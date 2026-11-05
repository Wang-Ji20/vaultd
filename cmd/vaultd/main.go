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
	"path/filepath"
	"slices"
	"syscall"

	"github.com/hortensia/vaultd/internal/config"
	"github.com/hortensia/vaultd/internal/index"
	"github.com/hortensia/vaultd/internal/server"
	"github.com/hortensia/vaultd/internal/vault"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: vaultd <command>

  run [--config path] [--stdio]    run the gateway in the foreground
  token new [--config path] <app>  create an app token and print it once
  reindex [--config path]          rebuild the search index from scratch
  version                          print the version
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
	case "token":
		return tokenCommand(args[1:], stdout, stderr)
	case "reindex":
		return reindexCommand(args[1:], stdout, stderr)
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
	store, err := openStore(*configPath)
	if err != nil {
		return err
	}
	// Stdout belongs to the MCP protocol in stdio mode, so logs go to stderr.
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, store, *stdio, logger)
}

// serve runs the gateway until ctx ends, then closes everything in order.
func serve(ctx context.Context, store *config.Store, stdio bool, logger *slog.Logger) error {
	v, ix, err := openVault(store.Get())
	if err != nil {
		return err
	}
	defer ix.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	gateway := server.New(server.Options{Store: store, Vault: v, Index: ix, Version: version, Logger: logger})
	if err := gateway.Start(ctx); err != nil {
		return err
	}
	defer gateway.Wait()
	if stdio {
		return gateway.RunStdio(ctx)
	}
	return gateway.Serve(ctx, nil)
}

func reindexCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("reindex", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to config.toml")
	if err := flags.Parse(args); err != nil {
		return err
	}
	store, err := openStore(*configPath)
	if err != nil {
		return err
	}
	_, ix, err := openVault(store.Get())
	if err != nil {
		return err
	}
	defer ix.Close()
	if err := ix.Rebuild(context.Background()); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "indexed %d notes\n", ix.State().Notes)
	return nil
}

func tokenCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "new" {
		return errors.New("usage: vaultd token new [--config path] <app-name>")
	}
	flags := flag.NewFlagSet("token new", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to config.toml")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: vaultd token new [--config path] <app-name>")
	}
	store, err := openStore(*configPath)
	if err != nil {
		return err
	}
	token, err := addApp(store, flags.Arg(0))
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, token)
	fmt.Fprintln(stderr, "Save this token now: it cannot be shown again.")
	return nil
}

func addApp(store *config.Store, name string) (string, error) {
	token, hash := config.NewToken()
	err := store.Update(func(c *config.Config) error {
		if slices.ContainsFunc(c.Apps, func(app config.App) bool { return app.Name == name }) {
			return fmt.Errorf("app %q already has a token", name)
		}
		c.Apps = append(c.Apps, config.App{Name: name, TokenSHA256: hash})
		return nil
	})
	return token, err
}

func openVault(cfg *config.Config) (*vault.Vault, *index.Index, error) {
	v, err := vault.New(server.VaultOptions(cfg))
	if err != nil {
		return nil, nil, err
	}
	dataDir, err := config.DefaultDataDir()
	if err != nil {
		return nil, nil, err
	}
	ix, err := index.Open(filepath.Join(dataDir, "index.db"), v)
	if err != nil {
		return nil, nil, err
	}
	return v, ix, nil
}

func openStore(path string) (*config.Store, error) {
	if path == "" {
		var err error
		if path, err = config.DefaultPath(); err != nil {
			return nil, err
		}
	}
	dataDir, err := config.DefaultDataDir()
	if err != nil {
		return nil, err
	}
	return config.OpenStore(path, dataDir)
}
