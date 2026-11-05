package server

import (
	"context"
	"net/http"

	"github.com/hortensia/vaultd/internal/audit"
	"github.com/hortensia/vaultd/internal/index"
	"github.com/hortensia/vaultd/internal/upstream"
	"github.com/hortensia/vaultd/internal/vault"
)

// These methods are what the admin portal uses to look at and steer the
// running gateway.

// MountAdmin serves handler at /admin. Call it before Serve.
func (g *Gateway) MountAdmin(handler http.Handler) { g.admin = handler }

// Vault returns the vault under the current config.
func (g *Gateway) Vault() *vault.Vault { return g.vault() }

// IndexState reports the search index's progress.
func (g *Gateway) IndexState() index.State { return g.index.State() }

// RebuildIndex starts a rebuild from scratch in the background. A failure is
// logged and shows in the portal as a low note count.
func (g *Gateway) RebuildIndex(ctx context.Context) {
	go func() {
		if err := g.index.Rebuild(ctx); err != nil {
			g.log.Error("index rebuild failed", "error", err)
		}
	}()
}

// Upstreams reports every upstream and its tools.
func (g *Gateway) Upstreams() []upstream.Info { return g.proxy.Status() }

// ConnectUpstreams connects to every upstream, so its tools can be listed.
func (g *Gateway) ConnectUpstreams(ctx context.Context) {
	g.proxy.ConnectAll(ctx)
	g.publishUpstreamTools()
}

// ReconnectUpstream drops an upstream's connection and connects again.
func (g *Gateway) ReconnectUpstream(ctx context.Context, name string) error {
	return g.proxy.Reconnect(ctx, name)
}

// AuditLog returns the audit log.
func (g *Gateway) AuditLog() *audit.Logger { return g.audit }
