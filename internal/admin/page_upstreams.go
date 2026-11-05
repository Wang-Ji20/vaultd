package admin

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/hortensia/vaultd/internal/config"
	"github.com/hortensia/vaultd/internal/upstream"
)

const upstreamPageWait = 3 * time.Second

func (p *Portal) upstreamsRoutes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /admin/upstreams":        p.upstreamsPage,
		"POST /admin/upstreams/tool":  p.upstreamTool,
		"POST /admin/upstreams/retry": p.upstreamReconnect,
	}
}

type upstreamsData struct {
	Upstreams []upstream.Info
}

func (p *Portal) upstreamsPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), upstreamPageWait)
	defer cancel()
	p.backend.ConnectUpstreams(ctx)
	p.showUpstreams(w, r, flash{})
}

func (p *Portal) showUpstreams(w http.ResponseWriter, r *http.Request, result flash) {
	p.show(w, r, "upstreams", "Upstreams", upstreamsData{Upstreams: p.backend.Upstreams()}, result)
}

func (p *Portal) upstreamTool(w http.ResponseWriter, r *http.Request) {
	name, tool := r.PostFormValue("name"), r.PostFormValue("tool")
	on := r.PostFormValue("on") != ""
	result := p.update(func(c *config.Config) error {
		return setToolAllowed(c, name, tool, on)
	}, "Saved "+name+"."+tool)
	p.showUpstreams(w, r, result)
}

func setToolAllowed(c *config.Config, name, tool string, on bool) error {
	i := slices.IndexFunc(c.Upstreams, func(u config.Upstream) bool { return u.Name == name })
	if i < 0 {
		return fmt.Errorf("no upstream named %q", name)
	}
	allow := slices.DeleteFunc(c.Upstreams[i].Allow, func(t string) bool { return t == tool })
	if on {
		allow = append(allow, tool) // validation refuses this if the tool is locked
	}
	c.Upstreams[i].Allow = allow
	return nil
}

func (p *Portal) upstreamReconnect(w http.ResponseWriter, r *http.Request) {
	name := r.PostFormValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), upstreamPageWait)
	defer cancel()
	result := flash{toast: "Reconnected " + name}
	if err := p.backend.ReconnectUpstream(ctx, name); err != nil {
		result = flash{err: "Could not reconnect " + name + ": " + err.Error()}
	}
	p.showUpstreams(w, r, result)
}
