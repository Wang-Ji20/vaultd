package server

import (
	"context"
	"time"

	"github.com/hortensia/vaultd/internal/vault"
)

const (
	recallDefaultLimit = 10
	recallMaxLimit     = 50
)

type appendInput struct {
	Path string `json:"path" jsonschema:"note path relative to the vault root; must be inside a writable folder"`
	Text string `json:"text" jsonschema:"text to add at the end of the note"`
}

type createInput struct {
	Path    string `json:"path" jsonschema:"path of the new note; must be inside a writable folder"`
	Content string `json:"content" jsonschema:"the note's full text"`
}

type createOutput struct {
	Path string `json:"path"`
}

type rememberInput struct {
	Text string `json:"text" jsonschema:"what to remember, as one line"`
}

type recallInput struct {
	Query string `json:"query" jsonschema:"plain text to search memories for"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of lines; default 10"`
}

type recallOutput struct {
	Lines    []string `json:"lines"`
	Indexing bool     `json:"indexing"`
}

func (g *Gateway) registerWriteTools() {
	addTool(g, "vault.append", "Add text to the end of a note, creating it if missing. Only works in writable folders. Cannot change existing text."+untrustedData, g.appendNote)
	addTool(g, "vault.create", "Create a new note. Only works in writable folders. Fails if the path is taken: it never overwrites."+untrustedData, g.createNote)
	addTool(g, "memory.remember", "Save a one-line memory for later, across apps and conversations."+untrustedData, g.remember)
	addTool(g, "memory.recall", "Search saved memories and return the matching lines."+untrustedData, g.recall)
}

func (g *Gateway) appendNote(ctx context.Context, app string, in appendInput) (*vault.AppendResult, error) {
	who := vault.WriteInfo{Tool: "vault.append", App: app}
	result, err := g.vault().Append(in.Path, in.Text, who, time.Now())
	if err != nil {
		return nil, err
	}
	g.refreshIndex(ctx, result.Path)
	return result, nil
}

func (g *Gateway) createNote(ctx context.Context, app string, in createInput) (createOutput, error) {
	who := vault.WriteInfo{Tool: "vault.create", App: app}
	path, err := g.vault().Create(in.Path, in.Content, who)
	if err != nil {
		return createOutput{}, err
	}
	g.refreshIndex(ctx, path)
	return createOutput{Path: path}, nil
}

func (g *Gateway) remember(ctx context.Context, app string, in rememberInput) (*vault.AppendResult, error) {
	who := vault.WriteInfo{Tool: "memory.remember", App: app}
	result, err := g.vault().Remember(in.Text, who, time.Now())
	if err != nil {
		return nil, err
	}
	g.refreshIndex(ctx, result.Path)
	return result, nil
}

func (g *Gateway) recall(ctx context.Context, _ string, in recallInput) (recallOutput, error) {
	lines, err := g.index.Recall(ctx, in.Query, clampLimit(in.Limit, recallDefaultLimit, recallMaxLimit))
	if err != nil {
		return recallOutput{}, err
	}
	return recallOutput{Lines: lines, Indexing: g.index.State().Indexing}, nil
}

// refreshIndex lets a model find what it just wrote, without waiting for the
// file watcher. The note is safely written by now, so a failure only delays
// search: the watcher will try again.
func (g *Gateway) refreshIndex(ctx context.Context, path string) {
	if err := g.index.IndexPath(ctx, path); err != nil {
		g.log.Warn("could not index a written note", "error", err)
	}
}
