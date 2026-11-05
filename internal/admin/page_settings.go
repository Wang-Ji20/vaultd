package admin

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/hortensia/vaultd/internal/config"
)

func (p *Portal) settingsRoutes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /admin/settings":  p.settingsPage,
		"POST /admin/settings": p.settingsSubmit,
	}
}

type settingsData struct {
	MaxFiles     int
	MaxSizeMB    int
	LogArguments bool
	SessionTTL   string
	Theme        string
}

func (p *Portal) settingsPage(w http.ResponseWriter, r *http.Request) {
	p.showSettings(w, r, flash{})
}

func (p *Portal) showSettings(w http.ResponseWriter, r *http.Request, result flash) {
	cfg := p.store.Get()
	theme := cfg.Admin.Theme
	if theme == "" {
		theme = "system"
	}
	p.show(w, r, "settings", "Settings", settingsData{
		MaxFiles:     cfg.Audit.MaxFiles,
		MaxSizeMB:    cfg.Audit.MaxSizeMB,
		LogArguments: cfg.Audit.LogArguments,
		SessionTTL:   cfg.Admin.SessionTTL.String(),
		Theme:        theme,
	}, result)
}

func (p *Portal) settingsSubmit(w http.ResponseWriter, r *http.Request) {
	maxFiles, filesErr := strconv.Atoi(r.PostFormValue("max_files"))
	maxSize, sizeErr := strconv.Atoi(r.PostFormValue("max_size_mb"))
	ttl, ttlErr := time.ParseDuration(r.PostFormValue("session_ttl"))
	if err := errors.Join(filesErr, sizeErr); err != nil {
		p.showSettings(w, r, flash{err: "Audit retention needs whole numbers."})
		return
	}
	if ttlErr != nil {
		p.showSettings(w, r, flash{err: "Session length must look like 12h or 90m."})
		return
	}
	result := p.update(func(c *config.Config) error {
		c.Audit.MaxFiles = maxFiles
		c.Audit.MaxSizeMB = maxSize
		c.Audit.LogArguments = r.PostFormValue("log_arguments") != ""
		c.Admin.SessionTTL = ttl
		c.Admin.Theme = r.PostFormValue("theme")
		return nil
	}, "Saved settings")
	p.showSettings(w, r, result)
}
