package admin

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/hortensia/vaultd/internal/config"
	"github.com/hortensia/vaultd/internal/index"
)

func (p *Portal) vaultRoutes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /admin/vault":          p.vaultPage,
		"POST /admin/vault/folder":  p.vaultFolder,
		"POST /admin/vault/git":     p.vaultGit,
		"POST /admin/vault/reindex": p.vaultReindex,
	}
}

type folderRow struct {
	Name     string
	ID       string
	Writable bool
}

type vaultData struct {
	Path          string
	Folders       []folderRow
	GitAutocommit bool
	GitStatus     string
	Index         index.State
}

func (p *Portal) vaultPage(w http.ResponseWriter, r *http.Request) {
	p.showVault(w, r, flash{})
}

func (p *Portal) showVault(w http.ResponseWriter, r *http.Request, result flash) {
	cfg := p.store.Get()
	v := p.backend.Vault()
	data := vaultData{
		Path:          cfg.Vault.Path,
		GitAutocommit: cfg.Vault.GitAutocommit,
		GitStatus:     v.GitStatus(),
		Index:         p.backend.IndexState(),
	}
	names := slices.Clone(cfg.Vault.WritableFolders)
	if listing, err := v.List(""); err == nil {
		names = append(names, listing.Folders...)
	} else if result.err == "" {
		result.err = err.Error()
	}
	slices.Sort(names)
	for i, name := range slices.Compact(names) {
		data.Folders = append(data.Folders, folderRow{
			Name:     name,
			ID:       "folder-" + strconv.Itoa(i),
			Writable: slices.Contains(cfg.Vault.WritableFolders, name),
		})
	}
	p.show(w, r, "vault", "Vault", data, result)
}

func (p *Portal) vaultFolder(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	on := r.PostFormValue("on") != ""
	if name == "" {
		p.showVault(w, r, flash{err: "Folder name must not be empty."})
		return
	}
	result := p.update(func(c *config.Config) error {
		folders := slices.DeleteFunc(c.Vault.WritableFolders, func(f string) bool { return f == name })
		if on {
			folders = append(folders, name)
		}
		c.Vault.WritableFolders = folders
		return nil
	}, "Saved writable folders")
	p.showVault(w, r, result)
}

func (p *Portal) vaultGit(w http.ResponseWriter, r *http.Request) {
	on := r.PostFormValue("on") != ""
	result := p.update(func(c *config.Config) error {
		c.Vault.GitAutocommit = on
		return nil
	}, "Saved git setting")
	p.showVault(w, r, result)
}

func (p *Portal) vaultReindex(w http.ResponseWriter, r *http.Request) {
	p.backend.RebuildIndex(p.base)
	p.showVault(w, r, flash{toast: "Rebuilding the index"})
}
