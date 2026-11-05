package admin

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/hortensia/vaultd/internal/config"
)

// pageData is what every template receives.
type pageData struct {
	Title   string
	Active  string // the sidebar entry to highlight
	CSRF    string
	Theme   string
	Version string
	Toast   string
	Error   string
	Data    any
}

type pageSet map[string]*template.Template

var pageNames = []string{
	"setup",
	"login",
	"overview",
}

func parsePages() (pageSet, error) {
	functions := template.FuncMap{"when": formatWhen, "dict": dict}
	pages := pageSet{}
	for _, name := range pageNames {
		page, err := template.New("layout.html").Funcs(functions).ParseFS(webFiles, "web/layout.html", "web/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("parse page %s: %w", name, err)
		}
		pages[name] = page
	}
	return pages, nil
}

// dict lets a template pass several named values to a nested template.
func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("dict needs key and value pairs")
	}
	out := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict key %v is not a string", pairs[i])
		}
		out[key] = pairs[i+1]
	}
	return out, nil
}

func formatWhen(stamp string) string {
	parsed, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return stamp
	}
	return parsed.Local().Format("2006-01-02 15:04:05")
}

// render buffers the page, so that a template error cannot leave half a page
// behind a success status.
func (p *Portal) render(w http.ResponseWriter, name string, data pageData) {
	data.Version = p.version
	if theme := p.store.Get().Admin.Theme; theme == "light" || theme == "dark" {
		data.Theme = theme
	}
	var buf bytes.Buffer
	if err := p.pages[name].Execute(&buf, data); err != nil {
		http.Error(w, "cannot show this page: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = buf.WriteTo(w)
}

func csrfOf(ctx context.Context) string {
	if current, ok := ctx.Value(sessionKey{}).(session); ok {
		return current.csrf
	}
	return ""
}

func (p *Portal) show(w http.ResponseWriter, r *http.Request, name, title string, data any, result flash) {
	p.render(w, name, pageData{
		Title: title, Active: name, CSRF: csrfOf(r.Context()),
		Toast: result.toast, Error: result.err, Data: data,
	})
}

// flash is the outcome of an action, shown when the page renders again.
type flash struct {
	toast string
	err   string
}

// update is the path of every change from the portal: validate, back up, save,
// hot reload. A refused change becomes an inline message and nothing is saved.
func (p *Portal) update(change func(*config.Config) error, success string) flash {
	if err := p.store.Update(change); err != nil {
		return flash{err: err.Error()}
	}
	return flash{toast: success}
}
