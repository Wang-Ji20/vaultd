package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/kardianos/service"
)

// serviceStopLimit is the 10 s for running calls, plus time to close the rest.
const serviceStopLimit = 15 * time.Second

// serviceConfig stores absolute paths in the service definition. A Windows
// Service runs as another account, whose default folders are not the user's.
func serviceConfig(where *paths) *service.Config {
	return &service.Config{
		Name:        "vaultd",
		DisplayName: "Vault Gateway (vaultd)",
		Description: "Local MCP gateway to an Obsidian vault and other MCP servers.",
		Arguments:   []string{"run", "--config", where.config, "--data-dir", where.data},
		// A systemd user service runs as the user, with access to the vault. On
		// Windows this option has no effect.
		Option: service.KeyValue{"UserService": true},
	}
}

func serviceCommand(action string, args []string, stdout, stderr io.Writer) error {
	flags, where := newFlags(action, stderr)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := where.resolve(); err != nil {
		return err
	}
	if action == "install" {
		// Better to fail now than leave a service that cannot start.
		if _, err := where.openStore(); err != nil {
			return err
		}
	}
	controlled, err := service.New(&program{where: where}, serviceConfig(where))
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	if err := service.Control(controlled, action); err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	fmt.Fprintf(stdout, "vaultd: %s done\n", action)
	return nil
}

// program is what the service manager starts and stops.
type program struct {
	where  *paths
	logger *slog.Logger
	cancel context.CancelFunc
	done   chan error
}

// Start must return quickly, so the gateway runs in the background.
func (p *program) Start(service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan error, 1)
	go func() { p.done <- serve(ctx, p.where, false, p.logger) }()
	return nil
}

// Stop shuts the gateway down gracefully, within the service manager's patience.
func (p *program) Stop(service.Service) error {
	p.cancel()
	select {
	case err := <-p.done:
		return err
	case <-time.After(serviceStopLimit):
		return fmt.Errorf("gateway did not stop within %v", serviceStopLimit)
	}
}

func runAsService(where *paths, stderr io.Writer) error {
	output, closeLog, err := serviceLogOutput(where.data, stderr)
	if err != nil {
		return err
	}
	defer closeLog()
	logger := slog.New(slog.NewTextHandler(output, nil))
	slog.SetDefault(logger)
	controlled, err := service.New(&program{where: where, logger: logger}, serviceConfig(where))
	if err != nil {
		return err
	}
	if err := controlled.Run(); err != nil {
		logger.Error("service stopped with an error", "error", err)
		return err
	}
	return nil
}

// serviceLogOutput keeps stderr where the system collects it, such as journald.
// A Windows Service has no stderr, so there the log goes to a file, replaced at
// each start so it cannot grow without limit.
func serviceLogOutput(dataDir string, stderr io.Writer) (io.Writer, func(), error) {
	if runtime.GOOS != "windows" {
		return stderr, func() {}, nil
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, nil, err
	}
	file, err := os.Create(filepath.Join(dataDir, "vaultd.log"))
	if err != nil {
		return nil, nil, err
	}
	return file, func() { file.Close() }, nil
}
