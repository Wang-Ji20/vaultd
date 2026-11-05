package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/hortensia/vaultd/internal/config"
)

func (p *Portal) appsRoutes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /admin/apps":         p.appsPage,
		"POST /admin/apps/new":    p.appNew,
		"POST /admin/apps/rotate": p.appRotate,
		"POST /admin/apps/revoke": p.appRevoke,
	}
}

type appRow struct {
	Name     string
	LastSeen string
	Calls    int
}

type snippet struct {
	Name string
	ID   string
	Hint string
	Text string
}

type appsData struct {
	Apps     []appRow
	NewApp   string
	NewToken string
	Snippets []snippet
	StatsErr string
}

func (p *Portal) appsPage(w http.ResponseWriter, r *http.Request) {
	p.showApps(w, r, "", "", flash{})
}

func (p *Portal) showApps(w http.ResponseWriter, r *http.Request, app, token string, result flash) {
	data := appsData{NewApp: app, NewToken: token, Snippets: clientSnippets(r.Host, token)}
	stats, err := p.backend.AuditLog().Stats()
	if err != nil {
		data.StatsErr = err.Error()
	}
	for _, configured := range p.store.Get().Apps {
		data.Apps = append(data.Apps, appRow{
			Name:     configured.Name,
			LastSeen: stats[configured.Name].LastSeen,
			Calls:    stats[configured.Name].Calls,
		})
	}
	p.show(w, r, "apps", "Apps & tokens", data, result)
}

func (p *Portal) appNew(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	token, hash := config.NewToken()
	result := p.update(func(c *config.Config) error {
		if slices.ContainsFunc(c.Apps, func(app config.App) bool { return app.Name == name }) {
			return fmt.Errorf("app %q already exists; use Rotate to replace its token", name)
		}
		c.Apps = append(c.Apps, config.App{Name: name, TokenSHA256: hash})
		return nil
	}, "Created "+name)
	p.showToken(w, r, name, token, result)
}

func (p *Portal) appRotate(w http.ResponseWriter, r *http.Request) {
	name := r.PostFormValue("name")
	token, hash := config.NewToken()
	result := p.update(func(c *config.Config) error {
		i := slices.IndexFunc(c.Apps, func(app config.App) bool { return app.Name == name })
		if i < 0 {
			return fmt.Errorf("no app named %q", name)
		}
		c.Apps[i].TokenSHA256 = hash
		return nil
	}, "New token for "+name)
	p.showToken(w, r, name, token, result)
}

// showToken shows a token only if it was really saved.
func (p *Portal) showToken(w http.ResponseWriter, r *http.Request, name, token string, result flash) {
	if result.err != "" {
		name, token = "", ""
	}
	p.showApps(w, r, name, token, result)
}

func (p *Portal) appRevoke(w http.ResponseWriter, r *http.Request) {
	name := r.PostFormValue("name")
	result := p.update(func(c *config.Config) error {
		c.Apps = slices.DeleteFunc(c.Apps, func(app config.App) bool { return app.Name == name })
		return nil
	}, "Revoked "+name)
	p.showApps(w, r, "", "", result)
}

// clientSnippets returns config for each client. Claude Desktop gets the stdio
// form, because its config file starts local commands; the others connect to the
// shared address.
func clientSnippets(host, token string) []snippet {
	if token == "" {
		token = "YOUR_TOKEN"
	}
	url := "http://" + host + "/mcp"
	bearer := "Bearer " + token
	exe, err := os.Executable()
	if err != nil {
		exe = "vaultd"
	}
	desktop, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{
		"vaultd": map[string]any{"command": exe, "args": []string{"run", "--stdio"}},
	}}, "", "  ")
	cursor, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{
		"vaultd": map[string]any{"url": url, "headers": map[string]string{"Authorization": bearer}},
	}}, "", "  ")
	return []snippet{
		{"Claude Desktop", "desktop", "claude_desktop_config.json (uses stdio, no token needed)", string(desktop)},
		{"Cursor", "cursor", "~/.cursor/mcp.json", string(cursor)},
		{"Claude Code", "claude-code", "run in a terminal", `claude mcp add --transport http vaultd ` + url + ` --header "Authorization: ` + bearer + `"`},
		{"DeepSeek Harness", "deepseek", "cordis.patch.yml", "mcp:\n  vaultd:\n    client: \"@deepseek-ai/dsh-mcp-client\"\n    transport: streamable-http\n    url: " + url + "\n    headers:\n      Authorization: " + bearer},
	}
}
