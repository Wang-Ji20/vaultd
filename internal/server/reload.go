package server

import (
	"context"
	"slices"

	"github.com/hortensia/vaultd/internal/config"
	"github.com/hortensia/vaultd/internal/vault"
)

// VaultOptions derives the vault's settings from a config.
func VaultOptions(cfg *config.Config) vault.Options {
	return vault.Options{Root: cfg.Vault.Path, Ignore: cfg.Vault.Ignore}
}

// Start begins indexing and applies config changes until ctx ends. Stop it by
// cancelling ctx, then calling Wait.
func (g *Gateway) Start(ctx context.Context) error {
	g.store.OnChange(func(old, updated *config.Config) { g.applyConfig(ctx, old, updated) })
	if err := g.store.Watch(ctx, g.log); err != nil {
		return err
	}
	return g.startIndex(ctx)
}

// Wait blocks until the background work begun by Start has stopped.
func (g *Gateway) Wait() {
	g.indexMu.Lock()
	defer g.indexMu.Unlock()
	if g.indexCancel != nil {
		g.indexCancel()
		<-g.indexDone
		g.indexCancel = nil
	}
}

func (g *Gateway) startIndex(ctx context.Context) error {
	g.indexMu.Lock()
	defer g.indexMu.Unlock()
	return g.startIndexLocked(ctx)
}

func (g *Gateway) startIndexLocked(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	done, err := g.index.Start(runCtx, g.log)
	if err != nil {
		cancel()
		return err
	}
	g.indexCancel, g.indexDone = cancel, done
	return nil
}

// applyConfig makes a new config take effect. Tokens need nothing here: they
// are read from the store on each request.
func (g *Gateway) applyConfig(ctx context.Context, old, updated *config.Config) {
	if old.Server.Listen != updated.Server.Listen {
		g.log.Warn("server.listen changed; restart vaultd to apply it")
	}
	if !vaultChanged(old, updated) {
		return
	}
	v, err := vault.New(VaultOptions(updated))
	if err != nil {
		g.log.Error("cannot open vault from new config; keeping the old one", "error", err)
		return
	}
	g.vaults.Store(v)
	if err := g.reindex(ctx, v); err != nil {
		g.log.Error("cannot restart indexing", "error", err)
	}
}

func vaultChanged(old, updated *config.Config) bool {
	return old.Vault.Path != updated.Vault.Path || !slices.Equal(old.Vault.Ignore, updated.Vault.Ignore)
}

// reindex points the index and its watcher at a new vault. It clears the index
// first: another vault can hold the same paths with other content, and a folder
// that is now ignored must not stay searchable while a sync runs.
func (g *Gateway) reindex(ctx context.Context, v *vault.Vault) error {
	g.indexMu.Lock()
	defer g.indexMu.Unlock()
	if g.indexCancel != nil {
		g.indexCancel()
		<-g.indexDone
		g.indexCancel = nil
	}
	if ctx.Err() != nil {
		return nil
	}
	g.index.SetVault(v)
	if err := g.index.Clear(ctx); err != nil {
		return err
	}
	return g.startIndexLocked(ctx)
}
