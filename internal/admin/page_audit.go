package admin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/hortensia/vaultd/internal/audit"
)

const auditPageSize = 50

func (p *Portal) auditRoutes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /admin/audit": p.auditPage,
	}
}

type auditData struct {
	Rows     []audit.Row
	App      string
	Tool     string
	Result   string
	Range    string
	Text     string
	Apps     []string
	Page     int
	Previous string
	Next     string
}

var auditRanges = map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}

func (p *Portal) auditPage(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	data := auditData{
		App: query.Get("app"), Tool: query.Get("tool"), Result: query.Get("result"),
		Range: query.Get("range"), Text: query.Get("text"), Apps: []string{audit.StdioName},
	}
	for _, app := range p.store.Get().Apps {
		data.Apps = append(data.Apps, app.Name)
	}
	number, err := strconv.Atoi(query.Get("page"))
	data.Page = max(number, 1)
	if err != nil {
		data.Page = 1
	}
	page, err := p.backend.AuditLog().Query(auditFilter(data, time.Now()), (data.Page-1)*auditPageSize, auditPageSize)
	result := flash{}
	if err != nil {
		result.err = err.Error()
	}
	data.Rows = page.Rows
	if data.Page > 1 {
		data.Previous = pageLink(r, data.Page-1)
	}
	if page.HasMore {
		data.Next = pageLink(r, data.Page+1)
	}
	p.show(w, r, "audit", "Audit log", data, result)
}

func auditFilter(data auditData, now time.Time) audit.Filter {
	filter := audit.Filter{App: data.App, Tool: data.Tool, Text: data.Text}
	switch data.Result {
	case "ok":
		yes := true
		filter.OK = &yes
	case "error":
		no := false
		filter.OK = &no
	}
	if window, found := auditRanges[data.Range]; found {
		filter.Since = now.Add(-window)
	}
	return filter
}

func pageLink(r *http.Request, page int) string {
	query := r.URL.Query()
	query.Set("page", strconv.Itoa(page))
	return "/admin/audit?" + query.Encode()
}
