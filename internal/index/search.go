package index

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hortensia/vaultd/internal/vault"
)

// searchSQL ranks with bm25. notes_fts has four columns and bm25 takes one
// weight per column, in order, so path comes first with weight 0. Its absence
// would shift the weights onto the wrong columns.
const searchSQL = `
SELECT n.path, n.title, n.tags, n.mtime,
       snippet(notes_fts, 3, '**', '**', '…', 20),
       bm25(notes_fts, 0.0, 10.0, 5.0, 1.0) AS rank
FROM notes_fts JOIN notes n ON n.rowid = notes_fts.rowid
WHERE notes_fts MATCH ?1 AND (?2 = '.' OR n.path = ?2 OR substr(n.path, 1, length(?2) + 1) = ?2 || '/')
ORDER BY rank LIMIT ?3`

// EscapeQuery turns plain user text into an FTS5 query in which every word is
// a quoted string, so nothing the user types can act as query syntax. All
// words must match. It returns "" if the text holds no words.
func EscapeQuery(text string) string {
	words := strings.Fields(strings.ReplaceAll(text, "\x00", " "))
	for i, word := range words {
		words[i] = `"` + strings.ReplaceAll(word, `"`, `""`) + `"`
	}
	return strings.Join(words, " ")
}

// Search returns the best matches for plain text, optionally only below a
// folder. Higher scores are better.
func (ix *Index) Search(ctx context.Context, text string, limit int, folder string) ([]vault.Hit, error) {
	query := EscapeQuery(text)
	if query == "" {
		return nil, errors.New("query must not be empty")
	}
	clean, err := vault.CleanRel(folder)
	if err != nil {
		return nil, err
	}
	rows, err := ix.db.QueryContext(ctx, searchSQL, query, clean, limit)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	defer rows.Close()
	hits := []vault.Hit{}
	for rows.Next() {
		var hit vault.Hit
		var tags string
		var mtime int64
		var rank float64
		if err := rows.Scan(&hit.Path, &hit.Title, &tags, &mtime, &hit.Snippet, &rank); err != nil {
			return nil, fmt.Errorf("search failed: %w", err)
		}
		if err := json.Unmarshal([]byte(tags), &hit.Tags); err != nil {
			return nil, fmt.Errorf("search failed: %w", err)
		}
		hit.Score = -rank // bm25 gives lower numbers to better matches
		hit.Modified = vault.FormatTime(time.Unix(0, mtime))
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	return hits, nil
}

// Recall returns lines of the memory file that match plain text, best first.
func (ix *Index) Recall(ctx context.Context, text string, limit int) ([]string, error) {
	query := EscapeQuery(text)
	if query == "" {
		return nil, errors.New("query must not be empty")
	}
	rows, err := ix.db.QueryContext(ctx, `SELECT line FROM memory_fts WHERE memory_fts MATCH ? ORDER BY rank LIMIT ?`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("recall failed: %w", err)
	}
	defer rows.Close()
	lines := []string{}
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, fmt.Errorf("recall failed: %w", err)
		}
		lines = append(lines, line)
	}
	return lines, rows.Err()
}

// Backlink is a note that links to another.
type Backlink struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Context string `json:"context"`
}

// Backlinks returns the notes linking to rel. Links are stored as written and
// resolved at query time, so a link starts or stops counting as soon as a
// better-matching note is created or deleted, without reindexing anything.
func (ix *Index) Backlinks(ctx context.Context, rel string) ([]Backlink, error) {
	notes, err := ix.allPaths(ctx)
	if err != nil {
		return nil, err
	}
	candidates := linkCandidates(rel)
	marks := strings.TrimSuffix(strings.Repeat("?,", len(candidates)), ",")
	args := make([]any, len(candidates))
	for i, candidate := range candidates {
		args[i] = candidate
	}
	rows, err := ix.db.QueryContext(ctx, `
		SELECT l.src, n.title, l.dst, l.line FROM links l JOIN notes n ON n.path = l.src
		WHERE l.dst IN (`+marks+`) ORDER BY l.src, l.rowid`, args...)
	if err != nil {
		return nil, fmt.Errorf("read backlinks: %w", err)
	}
	defer rows.Close()
	out := []Backlink{}
	for rows.Next() {
		var link Backlink
		var dst string
		if err := rows.Scan(&link.Path, &link.Title, &dst, &link.Context); err != nil {
			return nil, fmt.Errorf("read backlinks: %w", err)
		}
		if link.Path != rel && vault.ResolveLink(dst, notes) == rel {
			out = append(out, link)
		}
	}
	return out, rows.Err()
}

func (ix *Index) allPaths(ctx context.Context) ([]string, error) {
	rows, err := ix.db.QueryContext(ctx, `SELECT path FROM notes`)
	if err != nil {
		return nil, fmt.Errorf("read index: %w", err)
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("read index: %w", err)
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}
