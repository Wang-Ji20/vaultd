package admin

import (
	"net/http"
	"time"

	"github.com/hortensia/vaultd/internal/audit"
	"github.com/hortensia/vaultd/internal/index"
	"github.com/hortensia/vaultd/internal/upstream"
)

const recentCalls = 20

func (p *Portal) overviewRoutes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /admin/{$}":      p.overview,
		"POST /admin/logout":  p.logout,
		"POST /admin/restore": p.restore,
	}
}

type overviewData struct {
	Uptime    string
	VaultPath string
	Index     index.State
	Percent   int
	Upstreams []upstream.Info
	Calls     []audit.Row
	CallsErr  string
	Backups   []string
}

func (p *Portal) overview(w http.ResponseWriter, r *http.Request) {
	p.showOverview(w, r, flash{})
}

func (p *Portal) showOverview(w http.ResponseWriter, r *http.Request, result flash) {
	state := p.backend.IndexState()
	data := overviewData{
		Uptime:    time.Since(p.started).Round(time.Second).String(),
		VaultPath: p.store.Get().Vault.Path,
		Index:     state,
		Upstreams: p.backend.Upstreams(),
	}
	if state.Total > 0 {
		data.Percent = int(state.Done * 100 / state.Total)
	}
	page, err := p.backend.AuditLog().Query(audit.Filter{}, 0, recentCalls)
	if err != nil {
		data.CallsErr = err.Error()
	}
	data.Calls = page.Rows
	data.Backups, err = p.store.Backups()
	if err != nil && result.err == "" {
		result.err = err.Error()
	}
	p.show(w, r, "overview", "Overview", data, result)
}

func (p *Portal) restore(w http.ResponseWriter, r *http.Request) {
	name := r.PostFormValue("name")
	result := flash{toast: "Restored " + name}
	if err := p.store.Restore(name); err != nil {
		result = flash{err: err.Error()}
	}
	p.showOverview(w, r, result)
}
