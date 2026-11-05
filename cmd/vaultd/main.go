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

	"github.com/kardianos/service"

	"github.com/hortensia/vaultd/internal/admin"
	"github.com/hortensia/vaultd/internal/audit"
	"github.com/hortensia/vaultd/internal/config"
	"github.com/hortensia/vaultd/internal/index"
	"github.com/hortensia/vaultd/internal/server"
	"github.com/hortensia/vaultd/internal/vault"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: vaultd <command>

  run [--stdio]             run the gateway in the foreground
  init                      create a config and the first app token, by questions
  token new <app>           create an app token and print it once
  hash-password [--write]   prompt for the admin password; print its hash,
                            or save it in the config with --write
  reindex                   rebuild the search index from scratch
  install | uninstall       add or remove the OS service
  start | stop              start or stop the OS service
  version                   print the version

All commands except version accept --config <path> and --data-dir <path>.
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
	case "hash-password":
		return hashPasswordCommand(args[1:], os.Stdin, stdout, stderr)
	case "reindex":
		return reindexCommand(args[1:], stdout, stderr)
	case "init":
		return initCommand(args[1:], os.Stdin, stdout, stderr)
	case "install", "uninstall", "start", "stop":
		return serviceCommand(args[0], args[1:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, version)
		return nil
	}
	fmt.Fprint(stderr, usage)
	return fmt.Errorf("unknown command %q", args[0])
}

// paths holds the two locations every command needs. Flags override the
// platform's defaults.
type paths struct {
	config, data string
}

func addPathFlags(flags *flag.FlagSet) *paths {
	p := &paths{}
	flags.StringVar(&p.config, "config", "", "path to config.toml")
	flags.StringVar(&p.data, "data-dir", "", "folder for the index, audit log and backups")
	return p
}

// resolve fills in defaults and makes both paths absolute.
func (p *paths) resolve() error {
	var err error
	if p.config == "" {
		if p.config, err = config.DefaultPath(); err != nil {
			return err
		}
	}
	if p.data == "" {
		if p.data, err = config.DefaultDataDir(); err != nil {
			return err
		}
	}
	if p.config, err = filepath.Abs(p.config); err != nil {
		return err
	}
	p.data, err = filepath.Abs(p.data)
	return err
}

func (p *paths) openStore() (*config.Store, error) {
	if err := p.resolve(); err != nil {
		return nil, err
	}
	return config.OpenStore(p.config, p.data)
}

func newFlags(name string, stderr io.Writer) (*flag.FlagSet, *paths) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return flags, addPathFlags(flags)
}

func runCommand(args []string, stderr io.Writer) error {
	flags, where := newFlags("run", stderr)
	stdio := flags.Bool("stdio", false, "serve over standard input and output")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := where.resolve(); err != nil {
		return err
	}
	if !*stdio && !service.Interactive() {
		return runAsService(where, stderr)
	}
	// Stdout belongs to the MCP protocol in stdio mode, so logs go to stderr.
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, where, *stdio, logger)
}

// serve runs the gateway until ctx ends, then closes everything in order:
// requests first, then upstream children and the watcher, then the audit log
// and the database, which the deferred calls do in reverse.
func serve(ctx context.Context, where *paths, stdio bool, logger *slog.Logger) error {
	store, err := where.openStore()
	if err != nil {
		return err
	}
	v, ix, err := openVault(store.Get(), where.data)
	if err != nil {
		return err
	}
	defer ix.Close()
	auditLog, err := audit.Open(where.data, server.AuditSettings(store.Get()))
	if err != nil {
		return err
	}
	defer auditLog.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	gateway := server.New(server.Options{Store: store, Vault: v, Index: ix, Audit: auditLog, Version: version, Logger: logger})
	portal, err := admin.New(admin.Options{Store: store, Backend: gateway, Version: version, Base: ctx})
	if err != nil {
		return err
	}
	gateway.MountAdmin(portal)
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
	flags, where := newFlags("reindex", stderr)
	if err := flags.Parse(args); err != nil {
		return err
	}
	store, err := where.openStore()
	if err != nil {
		return err
	}
	_, ix, err := openVault(store.Get(), where.data)
	if err != nil {
		return err
	}
	defer ix.Close()
	if err := ix.Rebuild(context.Background()); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "notes indexed: %d\n", ix.State().Notes)
	return nil
}

func tokenCommand(args []string, stdout, stderr io.Writer) error {
	const tokenUsage = "usage: vaultd token new [--config path] <app-name>"
	if len(args) == 0 || args[0] != "new" {
		return errors.New(tokenUsage)
	}
	flags, where := newFlags("token new", stderr)
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New(tokenUsage)
	}
	store, err := where.openStore()
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

func openVault(cfg *config.Config, dataDir string) (*vault.Vault, *index.Index, error) {
	v, err := vault.New(server.VaultOptions(cfg))
	if err != nil {
		return nil, nil, err
	}
	ix, err := index.Open(filepath.Join(dataDir, "index.db"), v)
	if err != nil {
		return nil, nil, err
	}
	return v, ix, nil
}
