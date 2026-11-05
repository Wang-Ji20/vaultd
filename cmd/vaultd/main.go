// Command vaultd is a local MCP gateway to an Obsidian vault and to other MCP
// servers.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/hortensia/vaultd/internal/config"
	"github.com/hortensia/vaultd/internal/server"
	"github.com/hortensia/vaultd/internal/vault"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: vaultd <command>

  run [--config path] --stdio   serve the tools over standard input and output
  version                       print the version
`

func main() {
	if err := dispatch(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "vaultd:", err)
		os.Exit(1)
	}
}

func dispatch(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errors.New("no command given")
	}
	switch args[0] {
	case "run":
		return runCommand(args[1:], stderr)
	case "version":
		fmt.Fprintln(stdout, version)
		return nil
	}
	fmt.Fprint(stderr, usage)
	return fmt.Errorf("unknown command %q", args[0])
}

func runCommand(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to config.toml")
	stdio := flags.Bool("stdio", false, "serve over standard input and output")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*stdio {
		return errors.New("only --stdio is available so far")
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	// Stdout belongs to the MCP protocol in stdio mode, so logs go to stderr.
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	v, err := vault.New(vault.Options{Root: cfg.Vault.Path, Ignore: cfg.Vault.Ignore})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	gateway := server.New(server.Options{Vault: v, Version: version, Logger: logger})
	return gateway.RunStdio(ctx)
}

func loadConfig(path string) (*config.Config, error) {
	if path == "" {
		var err error
		if path, err = config.DefaultPath(); err != nil {
			return nil, err
		}
	}
	return config.Load(path)
}
