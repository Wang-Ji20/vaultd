package server

import (
	"context"

	"github.com/hortensia/vaultd/internal/vault"
)

const (
	searchDefaultLimit = 10
	searchMaxLimit     = 50
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

func (g *Gateway) registerVaultTools() {
	addTool(g, "vault.search", "Search the user's notes by plain text."+untrustedData, g.search)
	addTool(g, "vault.read", "Read one note: its title, frontmatter, tags and content. Refuses files over 1 MB."+untrustedData, g.read)
	addTool(g, "vault.list", "List the folders and notes directly inside a folder."+untrustedData, g.list)
}

func (g *Gateway) search(_ context.Context, _ string, in searchInput) (searchOutput, error) {
	hits, err := g.vault.ScanSearch(in.Query, clampLimit(in.Limit, searchDefaultLimit, searchMaxLimit), in.Folder)
	if err != nil {
		return searchOutput{}, err
	}
	return searchOutput{Results: hits}, nil
}

func (g *Gateway) read(_ context.Context, _ string, in readInput) (*vault.Note, error) {
	return g.vault.Read(in.Path)
}

func (g *Gateway) list(_ context.Context, _ string, in listInput) (*vault.Listing, error) {
	return g.vault.List(in.Folder)
}

// clampLimit applies a default to unset or negative values and a maximum.
func clampLimit(limit, fallback, maximum int) int {
	if limit <= 0 {
		return fallback
	}
	return min(limit, maximum)
}
