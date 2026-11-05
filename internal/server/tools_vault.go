package server

import (
	"context"
	"time"

	"github.com/hortensia/vaultd/internal/index"
	"github.com/hortensia/vaultd/internal/vault"
)

const (
	searchDefaultLimit = 10
	searchMaxLimit     = 50
	recentDefaultDays  = 7
	recentMaxDays      = 90
	recentDefaultLimit = 20
	recentMaxLimit     = 200
)

type searchInput struct {
	Query  string `json:"query" jsonschema:"plain text to search for"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum number of results; default 10, maximum 50"`
	Folder string `json:"folder,omitempty" jsonschema:"only search below this folder"`
}

type searchOutput struct {
	Results  []vault.Hit `json:"results"`
	Indexing bool        `json:"indexing"`
}

type readInput struct {
	Path string `json:"path" jsonschema:"note path relative to the vault root, using / as separator"`
}

type listInput struct {
	Folder string `json:"folder,omitempty" jsonschema:"folder relative to the vault root; default is the root"`
}

type recentInput struct {
	Days  int `json:"days,omitempty" jsonschema:"how many days to look back; default 7, maximum 90"`
	Limit int `json:"limit,omitempty" jsonschema:"maximum number of notes; default 20"`
}

type recentOutput struct {
	Notes []vault.NoteInfo `json:"notes"`
}

type backlinksOutput struct {
	Backlinks []index.Backlink `json:"backlinks"`
}

func (g *Gateway) registerVaultTools() {
	addTool(g, "vault.search", "Search the user's notes by plain text."+untrustedData, g.search)
	addTool(g, "vault.read", "Read one note: its title, frontmatter, tags and content. Refuses files over 1 MB."+untrustedData, g.read)
	addTool(g, "vault.list", "List the folders and notes directly inside a folder."+untrustedData, g.list)
	addTool(g, "vault.recent", "List recently modified notes, newest first."+untrustedData, g.recent)
	addTool(g, "vault.backlinks", "List the notes that link to a note, with the line holding each link."+untrustedData, g.backlinks)
}

func (g *Gateway) search(ctx context.Context, _ string, in searchInput) (searchOutput, error) {
	limit := clampLimit(in.Limit, searchDefaultLimit, searchMaxLimit)
	hits, err := g.index.Search(ctx, in.Query, limit, in.Folder)
	if err != nil {
		return searchOutput{}, err
	}
	return searchOutput{Results: hits, Indexing: g.index.State().Indexing}, nil
}

func (g *Gateway) read(_ context.Context, _ string, in readInput) (*vault.Note, error) {
	return g.vault.Read(in.Path)
}

func (g *Gateway) list(_ context.Context, _ string, in listInput) (*vault.Listing, error) {
	return g.vault.List(in.Folder)
}

func (g *Gateway) recent(_ context.Context, _ string, in recentInput) (recentOutput, error) {
	days := clampLimit(in.Days, recentDefaultDays, recentMaxDays)
	limit := clampLimit(in.Limit, recentDefaultLimit, recentMaxLimit)
	notes, err := g.vault.Recent(days, limit, time.Now())
	if err != nil {
		return recentOutput{}, err
	}
	return recentOutput{Notes: notes}, nil
}

func (g *Gateway) backlinks(ctx context.Context, _ string, in readInput) (backlinksOutput, error) {
	info, err := g.vault.Describe(in.Path) // the jail, and a clear error if missing
	if err != nil {
		return backlinksOutput{}, err
	}
	links, err := g.index.Backlinks(ctx, info.Path)
	if err != nil {
		return backlinksOutput{}, err
	}
	return backlinksOutput{Backlinks: links}, nil
}

// clampLimit applies a default to unset or negative values and a maximum.
func clampLimit(limit, fallback, maximum int) int {
	if limit <= 0 {
		return fallback
	}
	return min(limit, maximum)
}
