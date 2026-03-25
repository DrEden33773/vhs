package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/vhs/lexer"
	"github.com/charmbracelet/vhs/parser"
	version "github.com/hashicorp/go-version"
	"github.com/mattn/go-isatty"
	gap "github.com/muesli/go-app-paths"
	"github.com/spf13/cobra"
)

const extension = ".tape"

var (
	// Version stores the build version of VHS at the time of packaging through -ldflags.
	Version string

	// CommitSHA stores the commit SHA of VHS at the time of packaging through -ldflags.
	CommitSHA string

	ttydMinVersion = version.Must(version.NewVersion("1.7.2"))

	publishFlag bool
	outputs     *[]string

	quietFlag bool

	//nolint:wrapcheck
	rootCmd = &cobra.Command{
		Use:           "vhs <file>",
		Short:         "Run a given tape file and generates its outputs.",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true, // we print our own errors
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			log.SetFlags(0)
			if quietFlag {
				log.SetOutput(io.Discard)
			}
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			err := ensureDependencies()
			if err != nil {
				return err
			}

			in := cmd.InOrStdin()
			// Set the input to the file contents if a file is given
			// otherwise, use stdin
			if len(args) > 0 && args[0] != "-" {
				in, err = os.Open(args[0])
				if err != nil {
					return err
				}
				log.Println(GrayStyle.Render("File: " + args[0]))
			} else {
				stat, _ := os.Stdin.Stat()
				if (stat.Mode() & os.ModeCharDevice) != 0 {
					// The user ran vhs without any arguments or stdin.
					// Print the usage.
					return cmd.Help()
				}
			}

			input, err := io.ReadAll(in)
			if err != nil {
				return err
			}
			if string(input) == "" {
				return errors.New("no input provided")
			}

			var publishFile string
			out := cmd.OutOrStdout()
			if quietFlag {
				out = io.Discard
			}
			errs := Evaluate(cmd.Context(), string(input), out, func(v *VHS) {
				// Output is being overridden, prevent all outputs
				if len(*outputs) <= 0 {
					publishFile = v.Options.Video.Output.GIF
					return
				}

				for _, output := range *outputs {
					if strings.HasSuffix(output, gif) {
						v.Options.Video.Output.GIF = output
					} else if strings.HasSuffix(output, webm) {
						v.Options.Video.Output.WebM = output
					} else if strings.HasSuffix(output, mp4) {
						v.Options.Video.Output.MP4 = output
					}
				}

				publishFile = v.Options.Video.Output.GIF
			})

			publishEnv, publishEnvSet := os.LookupEnv("VHS_PUBLISH")
			if !publishEnvSet && !publishFlag && len(errs) == 0 {
				log.Println(FaintStyle.Render("Host your GIF on vhs.charm.sh: vhs publish <file>.gif"))
			}

			if len(errs) > 0 {
				printErrors(os.Stderr, string(input), errs)
				return errors.New("recording failed")
			}

			if (publishFlag || publishEnv == "true") && publishFile != "" {
				if isatty.IsTerminal(os.Stdout.Fd()) {
					log.Printf(GrayStyle.Render("Publishing %s... "), publishFile)
				}

				url, err := Publish(cmd.Context(), publishFile)
				if err != nil {
					return err
				}
				if quietFlag {
					cmd.Println(url)
					return nil
				}
				if isatty.IsTerminal(os.Stdout.Fd()) {
					log.Println(StringStyle.Render("Done!"))
					publishShareInstructions(url)
				}
				log.Println("  " + URLStyle.Render(url))
				if isatty.IsTerminal(os.Stdout.Fd()) {
					log.Println()
				}
			}

			return nil
		},
	}

	markdown  bool
	themesCmd = &cobra.Command{
		Use:   "themes",
		Short: "List all the available themes, one per line",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			var prefix, suffix string
			if markdown {
				log.Printf("# Themes\n\n")
				prefix, suffix = "* `", "`"
			}
			themes, err := sortedThemeNames()
			if err != nil {
				return err
			}
			for _, theme := range themes {
				log.Printf("%s%s%s\n", prefix, theme, suffix)
			}
			return nil
		},
	}

	shell     string
	recordCmd = &cobra.Command{
		Use:   "record",
		Short: "Create a new tape file by recording your actions",
		Args:  cobra.NoArgs,
		RunE:  Record,
	}

	//nolint:wrapcheck
	newCmd = &cobra.Command{
		Use:   "new <name>",
		Short: "Create a new tape file with example tape file contents and documentation",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := strings.TrimSuffix(args[0], extension)
			fileName := name + extension

			f, err := os.Create(fileName)
			if err != nil {
				return err
			}

			_, err = f.Write(bytes.Replace(demoTape, []byte("examples/demo.gif"), []byte(name+".gif"), 1))
			if err != nil {
				return err
			}

			log.Println("Created " + fileName)

			return nil
		},
	}

	validateCmd = &cobra.Command{
		Use:   "validate <file>...",
		Short: "Validate a glob file path and parses all the files to ensure they are valid without running them.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			valid := true

			for _, file := range args {
				b, err := os.ReadFile(file)
				if err != nil {
					continue
				}

				l := lexer.New(string(b))
				p := parser.New(l)

				_ = p.Parse()
				errs := p.Errors()

				if len(errs) != 0 {
					log.Println(ErrorFileStyle.Render(file))

					for _, err := range errs {
						printError(os.Stderr, string(b), err)
					}
					valid = false
				}
			}

			if !valid {
				return errors.New("invalid tape file(s)")
			}

			return nil
		},
	}
)

func main() {
	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt, syscall.SIGTERM,
	)
	defer cancel()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.Flags().BoolVarP(&publishFlag, "publish", "p", false, "publish your GIF to vhs.charm.sh and get a shareable URL")
	rootCmd.PersistentFlags().BoolVarP(&quietFlag, "quiet", "q", false, "quiet do not log messages. If publish flag is provided, it will log shareable URL")

	outputs = rootCmd.Flags().StringSliceP("output", "o", []string{}, "file name(s) of video output")
	themesCmd.Flags().BoolVar(&markdown, "markdown", false, "output as markdown")
	_ = themesCmd.Flags().MarkHidden("markdown")
	recordShell := filepath.Base(os.Getenv("SHELL"))
	if recordShell == "" {
		recordShell = defaultShell
	}
	recordCmd.Flags().StringVarP(&shell, "shell", "s", recordShell, "shell for recording")
	rootCmd.AddCommand(
		recordCmd,
		newCmd,
		themesCmd,
		validateCmd,
		manCmd,
		serveCmd,
		publishCmd,
		daemonCmd,
	)
	rootCmd.CompletionOptions.HiddenDefaultCmd = true

	if len(CommitSHA) >= 7 { //nolint:mnd
		vt := rootCmd.VersionTemplate()
		rootCmd.SetVersionTemplate(vt[:len(vt)-1] + " (" + CommitSHA[0:7] + ")\n")
	}
	if Version == "" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Sum != "" {
			Version = info.Main.Version
		} else {
			Version = "unknown (built from source)"
		}
	}
	rootCmd.Version = Version
}

var versionRegex = regexp.MustCompile(`\d+\.\d+\.\d+`)

// getVersion returns the parsed version of a program.
func getVersion(program string) *version.Version {
	cmd := exec.Command(program, "--version")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	programVersion, _ := version.NewVersion(versionRegex.FindString(string(out)))
	return programVersion
}

// depsCache stores the result of a successful dependency check.
type depsCache struct {
	TTYDVersion string `json:"ttyd_version"`
	Timestamp   int64  `json:"timestamp"`
}

// depsCacheTTL is how long the dependency check cache remains valid.
const depsCacheTTL = 1 * time.Hour

// depsCachePath returns the path to the dependency check cache file.
func depsCachePath() (string, error) {
	scope := gap.NewScope(gap.User, "vhs")
	dataPath, err := scope.DataPath("deps_check.json")
	if err != nil {
		return "", fmt.Errorf("could not determine cache path: %w", err)
	}
	return dataPath, nil
}

// readDepsCache reads and validates the dependency check cache.
// It returns nil if the cache is missing, expired, or invalid.
func readDepsCache() *depsCache {
	path, err := depsCachePath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cache depsCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil
	}
	if time.Since(time.Unix(cache.Timestamp, 0)) > depsCacheTTL {
		return nil
	}
	return &cache
}

// writeDepsCache persists the dependency check result to the cache file.
func writeDepsCache(ttydVer string) {
	path, err := depsCachePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return
	}
	data, err := json.Marshal(depsCache{
		TTYDVersion: ttydVer,
		Timestamp:   time.Now().Unix(),
	})
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

// ensureDependencies ensures that all dependencies are correctly installed
// and versioned before continuing.
//
// Results are cached to avoid repeated file system lookups and process
// invocations on subsequent runs (see depsCacheTTL).
func ensureDependencies() error {
	// Fast path: use cached result when available.
	if cache := readDepsCache(); cache != nil {
		return nil
	}

	_, ffmpegErr := exec.LookPath("ffmpeg")
	if ffmpegErr != nil {
		return fmt.Errorf("ffmpeg is not installed. Install it from: http://ffmpeg.org")
	}
	_, ttydErr := exec.LookPath("ttyd")
	if ttydErr != nil {
		return fmt.Errorf("ttyd is not installed. Install it from: https://github.com/tsl0922/ttyd")
	}
	_, shellErr := exec.LookPath(defaultShell)
	if shellErr != nil {
		return fmt.Errorf("%v is not installed", defaultShell)
	}

	ttydVersion := getVersion("ttyd")
	if ttydVersion == nil || ttydVersion.LessThan(ttydMinVersion) {
		return fmt.Errorf("ttyd version (%s) is out of date, VHS requires %s\n%s",
			ttydVersion,
			ttydMinVersion,
			"Install the latest version from: https://github.com/tsl0922/ttyd")
	}

	// Cache the successful result for future runs.
	writeDepsCache(ttydVersion.String())

	return nil
}
