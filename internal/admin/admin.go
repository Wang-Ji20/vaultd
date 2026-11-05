// Package admin serves the local web portal for managing the gateway.
package admin

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"time"

	"github.com/hortensia/vaultd/internal/audit"
	"github.com/hortensia/vaultd/internal/config"
	"github.com/hortensia/vaultd/internal/index"
	"github.com/hortensia/vaultd/internal/upstream"
	"github.com/hortensia/vaultd/internal/vault"
)

//go:embed web
var webFiles embed.FS

const maxFormBytes = 1 << 20

// Backend is what the portal needs from the running gateway.
type Backend interface {
	Vault() *vault.Vault
	IndexState() index.State
	RebuildIndex(ctx context.Context)
	Upstreams() []upstream.Info
	ConnectUpstreams(ctx context.Context)
	ReconnectUpstream(ctx context.Context, name string) error
	AuditLog() *audit.Logger
}

// Options configures the portal.
type Options struct {
	Store   *config.Store
	Backend Backend
	Version string
	// Base outlives single requests, for work such as an index rebuild.
	Base context.Context
}

// Portal is the admin portal's HTTP handler.
type Portal struct {
	store    *config.Store
	backend  Backend
	version  string
	base     context.Context
	started  time.Time
	sessions *sessionStore
	limiter  *loginLimiter
	pages    pageSet
	mux      *http.ServeMux
}

// New builds the portal.
func New(opts Options) (*Portal, error) {
	pages, err := parsePages()
	if err != nil {
		return nil, err
	}
	p := &Portal{
		store:    opts.Store,
		backend:  opts.Backend,
		version:  opts.Version,
		base:     opts.Base,
		started:  time.Now(),
		sessions: newSessionStore(),
		limiter:  newLoginLimiter(),
		pages:    pages,
		mux:      http.NewServeMux(),
	}
	p.routes()
	return p, nil
}

func (p *Portal) routes() {
	static, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err) // the embedded folder is part of the binary
	}
	p.mux.Handle("GET /admin/static/", http.StripPrefix("/admin/static/", http.FileServerFS(static)))
	p.mux.Handle("/admin", http.RedirectHandler("/admin/", http.StatusSeeOther))
	p.handle("GET /admin/login", p.loginPage)
	p.handle("POST /admin/login", p.loginSubmit)

	for _, pages := range []map[string]http.HandlerFunc{
		p.overviewRoutes(),
		p.appsRoutes(),
	} {
		for pattern, handler := range pages {
			p.handle(pattern, p.requireSession(handler))
		}
	}
}

// handle adds the checks every non-static page shares.
func (p *Portal) handle(pattern string, handler http.HandlerFunc) {
	p.mux.HandleFunc(pattern, p.requirePasswordSet(handler))
}

// ServeHTTP sets security headers, then routes. It looks only at the session
// cookie: an Authorization header gets no special treatment, so app tokens
// open nothing here.
func (p *Portal) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Content-Security-Policy", "default-src 'self'")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	p.mux.ServeHTTP(w, r)
}
