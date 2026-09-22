package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sol12378/keiba-masters-kit/internal/voting"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("votingd stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	rootFlag := flag.String("project-root", "", "repository root; auto-detected when empty")
	policyFlag := flag.String("policy", "configs/voting_policy.json", "voting policy path")
	driverFlag := flag.String("driver", voting.DriverPaper, "submission driver: paper (offline, default) or live (official contest endpoint)")
	flag.Parse()

	root := *rootFlag
	if root == "" {
		var err error
		root, err = voting.FindProjectRoot()
		if err != nil {
			return err
		}
	}
	config, err := voting.LoadRuntimeConfig(root, *policyFlag)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.StateDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(config.StateDir, 0o700); err != nil {
		return err
	}
	// Acquire the control socket before opening mutable state. The socket is
	// also the process-wide single-writer lock for this policy/state directory.
	controlListener, err := voting.ListenControlSocket(config.Socket)
	if err != nil {
		return err
	}
	defer func() {
		_ = controlListener.Close()
		_ = os.Remove(config.Socket)
	}()
	store, err := voting.OpenStore(config, time.Now)
	if err != nil {
		return err
	}
	client, err := voting.NewDriver(*driverFlag, config)
	if err != nil {
		return err
	}
	logger.Info("submission driver selected", "driver", *driverFlag)
	service := voting.NewService(config, store, client, voting.CredentialsFor(*driverFlag), logger, time.Now)
	httpService := voting.NewHTTPService(service, config, voting.BuildInfo{
		Version: version, Commit: commit, BuildTime: buildTime,
	})
	statusServer := voting.NewStatusServer(config, httpService.StatusHandler())
	controlServer := &http.Server{
		Handler:           httpService.ControlHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errorsChannel := make(chan error, 3)
	go func() {
		logger.Info("status server starting", "listen", config.Policy.Interfaces.StatusListen, "read_only", true)
		err := statusServer.ListenAndServe()
		if !errors.Is(err, http.ErrServerClosed) {
			errorsChannel <- err
		}
	}()
	go func() {
		logger.Info("control server starting", "socket", config.Socket, "mode", "0600")
		err := controlServer.Serve(controlListener)
		if !errors.Is(err, http.ErrServerClosed) {
			errorsChannel <- err
		}
	}()
	go func() { errorsChannel <- service.Run(ctx) }()

	select {
	case <-ctx.Done():
	case err := <-errorsChannel:
		if err != nil {
			stop()
			shutdownContext, cancel := context.WithTimeout(context.Background(), time.Duration(config.Policy.Timing.ShutdownGraceSeconds)*time.Second)
			defer cancel()
			_ = voting.ShutdownServers(shutdownContext, statusServer, controlServer)
			return err
		}
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Duration(config.Policy.Timing.ShutdownGraceSeconds)*time.Second)
	defer cancel()
	return voting.ShutdownServers(shutdownContext, statusServer, controlServer)
}
