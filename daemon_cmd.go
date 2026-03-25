package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/kardianos/service"
	"github.com/spf13/cobra"
)

// daemonService wraps Daemon to satisfy the service.Interface required by
// kardianos/service for cross-platform service management.
type daemonService struct {
	daemon *Daemon
}

func (ds *daemonService) Start(_ service.Service) error {
	return ds.daemon.Start()
}

func (ds *daemonService) Stop(_ service.Service) error {
	return ds.daemon.Stop()
}

// serviceName is the identifier used when registering VHS as a system service.
const serviceName = "vhs-daemon"

// newServiceConfig returns the kardianos/service configuration for the VHS
// daemon.
func newServiceConfig() *service.Config {
	return &service.Config{
		Name:        serviceName,
		DisplayName: "VHS Browser Daemon",
		Description: "Keeps a headless Chromium instance running to speed up VHS recordings.",
		Arguments:   []string{"daemon", "run"},
		Option: service.KeyValue{
			// Install as a user-level service where supported (systemd, launchd).
			"UserService": true,
		},
	}
}

var (
	daemonCmd = &cobra.Command{
		Use:   "daemon",
		Short: "Manage the VHS browser daemon for faster recordings",
		Long: `The VHS daemon keeps a headless Chromium browser running in the background.
When a daemon is available, VHS reuses the existing browser instead of launching
a new one, significantly reducing startup time.`,
	}

	daemonStartCmd = &cobra.Command{
		Use:   "start",
		Short: "Start the browser daemon in the background",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			svc, err := newDaemonService()
			if err != nil {
				return err
			}
			if err := svc.Start(); err != nil {
				return fmt.Errorf("could not start daemon service: %w", err)
			}
			log.Println("daemon service started")
			return nil
		},
	}

	daemonStopCmd = &cobra.Command{
		Use:   "stop",
		Short: "Stop the browser daemon",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			svc, err := newDaemonService()
			if err != nil {
				return err
			}
			if err := svc.Stop(); err != nil {
				return fmt.Errorf("could not stop daemon service: %w", err)
			}
			log.Println("daemon service stopped")
			return nil
		},
	}

	daemonStatusCmd = &cobra.Command{
		Use:   "status",
		Short: "Show the status of the browser daemon",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			svc, err := newDaemonService()
			if err != nil {
				return err
			}
			status, err := svc.Status()
			if err != nil {
				return fmt.Errorf("could not get daemon status: %w", err)
			}
			switch status {
			case service.StatusRunning:
				log.Println("daemon is running")
			case service.StatusStopped:
				log.Println("daemon is stopped")
			default:
				log.Println("daemon status is unknown")
			}
			return nil
		},
	}

	// daemonRunCmd is the actual foreground entry-point invoked by the service
	// manager. End-users normally do not call this directly.
	daemonRunCmd = &cobra.Command{
		Use:    "run",
		Short:  "Run the daemon in the foreground (used by the service manager)",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			svc, err := newDaemonService()
			if err != nil {
				return err
			}

			// When running interactively (not under a service manager), handle
			// signals ourselves so "vhs daemon run" can also be used directly.
			if service.Interactive() {
				return runInteractive(svc)
			}

			return svc.SystemService().Run()
		},
	}

	daemonInstallCmd = &cobra.Command{
		Use:   "install",
		Short: "Register the daemon as a system service",
		Long: `Register the VHS daemon as a system service so it starts automatically.

On Linux this creates a systemd user service.
On macOS this creates a launchd LaunchAgent.
On Windows this registers a Windows Service.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			svc, err := newDaemonService()
			if err != nil {
				return err
			}
			if err := svc.SystemService().Install(); err != nil {
				return fmt.Errorf("could not install daemon service: %w", err)
			}
			log.Println("daemon service installed")
			return nil
		},
	}

	daemonUninstallCmd = &cobra.Command{
		Use:   "uninstall",
		Short: "Unregister the daemon system service",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			svc, err := newDaemonService()
			if err != nil {
				return err
			}
			if err := svc.SystemService().Uninstall(); err != nil {
				return fmt.Errorf("could not uninstall daemon service: %w", err)
			}
			removeDaemonURL()
			log.Println("daemon service uninstalled")
			return nil
		},
	}
)

// daemonSvc bundles a kardianos service with a typed reference to the
// underlying daemon so callers can interact with both layers.
type daemonSvc struct {
	svc service.Service
	ds  *daemonService
}

// newDaemonService creates a kardianos service backed by a Daemon.
func newDaemonService() (*daemonSvc, error) {
	ds := &daemonService{daemon: NewDaemon()}
	svc, err := service.New(ds, newServiceConfig())
	if err != nil {
		return nil, fmt.Errorf("could not create service: %w", err)
	}
	return &daemonSvc{svc: svc, ds: ds}, nil
}

// Start starts the daemon through the service manager.
func (s *daemonSvc) Start() error {
	return s.svc.Start() //nolint:wrapcheck
}

// Stop stops the daemon through the service manager.
func (s *daemonSvc) Stop() error {
	return s.svc.Stop() //nolint:wrapcheck
}

// Status returns the service status.
func (s *daemonSvc) Status() (service.Status, error) {
	return s.svc.Status() //nolint:wrapcheck
}

// SystemService returns the underlying kardianos service for install/uninstall.
func (s *daemonSvc) SystemService() service.Service {
	return s.svc
}

// runInteractive runs the daemon in the foreground, blocking until interrupted.
func runInteractive(ds *daemonSvc) error {
	if err := ds.ds.Start(ds.svc); err != nil {
		return err
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	return ds.ds.Stop(ds.svc)
}

func init() {
	daemonCmd.AddCommand(
		daemonStartCmd,
		daemonStopCmd,
		daemonStatusCmd,
		daemonRunCmd,
		daemonInstallCmd,
		daemonUninstallCmd,
	)
}
