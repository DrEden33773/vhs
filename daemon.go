package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	gap "github.com/muesli/go-app-paths"
)

// daemonURLFile is the name of the file that stores the daemon's CDP URL.
const daemonURLFile = "daemon.url"

// Daemon manages a long-running Chromium process that can be shared across
// multiple VHS invocations to avoid the cold-start cost of launching a new
// browser each time.
type Daemon struct {
	mu         sync.Mutex
	browser    *rod.Browser
	browserURL string
}

// NewDaemon creates a new Daemon instance.
func NewDaemon() *Daemon {
	return &Daemon{}
}

// Start launches a headless Chromium process and persists the CDP WebSocket
// URL to a well-known file so that VHS clients can discover it.
func (d *Daemon) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.browser != nil {
		return fmt.Errorf("daemon is already running")
	}

	path, _ := launcher.LookPath()
	enableNoSandbox := os.Getenv("VHS_NO_SANDBOX") != ""
	u, err := launcher.New().Leakless(false).Bin(path).NoSandbox(enableNoSandbox).Launch()
	if err != nil {
		return fmt.Errorf("could not launch browser: %w", err)
	}

	browser := rod.New().ControlURL(u)
	if err := browser.Connect(); err != nil {
		return fmt.Errorf("could not connect to browser: %w", err)
	}

	d.browser = browser
	d.browserURL = u

	if err := writeDaemonURL(u); err != nil {
		log.Printf("warning: could not persist daemon URL: %v", err)
	}

	log.Printf("daemon started, CDP URL: %s", u) //nolint:gosec
	return nil
}

// Stop gracefully shuts down the browser and removes the persisted URL file.
func (d *Daemon) Stop() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.browser == nil {
		return fmt.Errorf("daemon is not running")
	}

	if err := d.browser.Close(); err != nil {
		return fmt.Errorf("could not close browser: %w", err)
	}

	d.browser = nil
	d.browserURL = ""
	removeDaemonURL()

	log.Println("daemon stopped")
	return nil
}

// URL returns the CDP WebSocket URL of the running browser.
func (d *Daemon) URL() string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.browserURL
}

// Health checks whether the browser is still responsive by attempting to
// create and immediately close a page.
func (d *Daemon) Health() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.browser == nil {
		return fmt.Errorf("daemon is not running")
	}

	// A lightweight connectivity check.
	page, err := d.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return fmt.Errorf("browser is not responsive: %w", err)
	}
	_ = page.Close()
	return nil
}

// daemonURLPath returns the path to the file that stores the daemon's CDP URL.
func daemonURLPath() (string, error) {
	scope := gap.NewScope(gap.User, "vhs")
	p, err := scope.DataPath(daemonURLFile)
	if err != nil {
		return "", fmt.Errorf("could not determine daemon URL path: %w", err)
	}
	return p, nil
}

// writeDaemonURL writes the CDP URL to the daemon URL file.
func writeDaemonURL(u string) error {
	p, err := daemonURLPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return fmt.Errorf("could not create daemon URL directory: %w", err)
	}
	if err := os.WriteFile(p, []byte(u), 0o600); err != nil { //nolint:gosec
		return fmt.Errorf("could not write daemon URL file: %w", err)
	}
	return nil
}

// readDaemonURL reads the CDP URL from the daemon URL file.
// It returns an empty string and an error if the file does not exist or is
// empty.
func readDaemonURL() (string, error) {
	p, err := daemonURLPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("could not read daemon URL file: %w", err)
	}
	u := strings.TrimSpace(string(data))
	if u == "" {
		return "", fmt.Errorf("daemon URL file is empty")
	}
	return u, nil
}

// removeDaemonURL removes the daemon URL file.
func removeDaemonURL() {
	p, err := daemonURLPath()
	if err != nil {
		return
	}
	_ = os.Remove(p)
}

// probeDaemon attempts to connect to an existing daemon and returns its CDP
// URL if the daemon is healthy. It returns an error if no daemon is running
// or the daemon is not responsive.
func probeDaemon() (string, error) {
	u, err := readDaemonURL()
	if err != nil {
		return "", err
	}

	// Verify the daemon is actually responsive by connecting and pinging.
	browser := rod.New().ControlURL(u)
	if err := browser.Connect(); err != nil {
		removeDaemonURL()
		return "", fmt.Errorf("daemon at %s is not reachable: %w", u, err)
	}

	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		_ = browser.Close()
		removeDaemonURL()
		return "", fmt.Errorf("daemon browser is not responsive: %w", err)
	}
	_ = page.Close()

	// Disconnect our probe connection — the real VHS session will reconnect.
	_ = browser.Close()
	return u, nil
}
