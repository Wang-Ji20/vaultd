package index

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/hortensia/vaultd/internal/vault"
)

// batchSize is how many notes share a transaction. Readers see each finished
// batch, which is how search returns partial results during indexing.
const batchSize = 500

type fileState struct {
	mtime, size int64
}

// Sync brings the index in line with the vault: it indexes new and changed
// notes and deletes rows of notes that are gone.
func (ix *Index) Sync(ctx context.Context) error {
	ix.syncMu.Lock()
	defer ix.syncMu.Unlock()
	return ix.syncLocked(ctx)
}

// Rebuild empties the index and indexes the whole vault again.
func (ix *Index) Rebuild(ctx context.Context) error {
	ix.syncMu.Lock()
	defer ix.syncMu.Unlock()
	ix.indexing.Store(true) // searches must not report "ready" on an empty index
	if err := ix.clearLocked(ctx); err != nil {
		ix.indexing.Store(false)
		return err
	}
	return ix.syncLocked(ctx)
}

// Clear empties the index.
func (ix *Index) Clear(ctx context.Context) error {
	ix.syncMu.Lock()
	defer ix.syncMu.Unlock()
	return ix.clearLocked(ctx)
}

func (ix *Index) clearLocked(ctx context.Context) error {
	err := ix.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM notes; DELETE FROM notes_fts; DELETE FROM links; DELETE FROM memory_fts;`)
		return err
	})
	if err != nil {
		return fmt.Errorf("clear index: %w", err)
	}
	return nil
}

func (ix *Index) syncLocked(ctx context.Context) error {
	ix.indexing.Store(true)
	defer ix.indexing.Store(false)
	known, err := ix.knownFiles(ctx)
	if err != nil {
		return err
	}
	changed, total, err := ix.findChanges(known)
	if err != nil {
		return err
	}
	ix.total.Store(int64(total))
	ix.done.Store(int64(total - len(changed)))
	if err := ix.indexBatches(ctx, changed); err != nil {
		return err
	}
	return ix.deleteMissing(ctx, known)
}

func (ix *Index) knownFiles(ctx context.Context) (map[string]fileState, error) {
	rows, err := ix.db.QueryContext(ctx, `SELECT path, mtime, size FROM notes`)
	if err != nil {
		return nil, fmt.Errorf("read index: %w", err)
	}
	defer rows.Close()
	known := map[string]fileState{}
	for rows.Next() {
		var p string
		var state fileState
		if err := rows.Scan(&p, &state.mtime, &state.size); err != nil {
			return nil, fmt.Errorf("read index: %w", err)
		}
		known[p] = state
	}
	return known, rows.Err()
}

// findChanges walks the vault. Afterwards, known holds only rows whose notes
// no longer exist.
func (ix *Index) findChanges(known map[string]fileState) (changed []string, total int, err error) {
	err = ix.vault().WalkNotes(func(rel string, info fs.FileInfo) error {
		total++
		state, seen := known[rel]
		delete(known, rel)
		if !seen || state.mtime != info.ModTime().UnixNano() || state.size != info.Size() {
			changed = append(changed, rel)
		}
		return nil
	})
	return changed, total, err
}

func (ix *Index) indexBatches(ctx context.Context, paths []string) error {
	for start := 0; start < len(paths); start += batchSize {
		batch := paths[start:min(start+batchSize, len(paths))]
		err := ix.write(ctx, func(tx *sql.Tx) error {
			for _, rel := range batch {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := indexNote(tx, ix.vault(), rel); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("index notes: %w", err)
		}
		ix.done.Add(int64(len(batch)))
	}
	return nil
}

func (ix *Index) deleteMissing(ctx context.Context, missing map[string]fileState) error {
	return ix.write(ctx, func(tx *sql.Tx) error {
		v := ix.vault()
		for rel := range missing {
			if err := removeNote(tx, v, rel); err != nil {
				return fmt.Errorf("remove %q from index: %w", rel, err)
			}
		}
		return nil
	})
}

// IndexPath updates the index for one note after a change. A note that is
// gone or no longer allowed is removed.
func (ix *Index) IndexPath(ctx context.Context, rel string) error {
	return ix.write(ctx, func(tx *sql.Tx) error {
		return indexNote(tx, ix.vault(), rel)
	})
}

// RemoveTree removes a note or every note below a folder.
func (ix *Index) RemoveTree(ctx context.Context, rel string) error {
	return ix.write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT path FROM notes WHERE path = ? OR substr(path, 1, ?) = ?`, rel, len(rel)+1, rel+"/")
		if err != nil {
			return err
		}
		var paths []string
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return err
			}
			paths = append(paths, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		v := ix.vault()
		for _, p := range paths {
			if err := removeNote(tx, v, p); err != nil {
				return err
			}
		}
		return nil
	})
}

// indexNote reads one note and stores it. An unreadable note is removed, so
// the index never serves text that the jail would now refuse.
func indexNote(tx *sql.Tx, v *vault.Vault, rel string) error {
	raw, info, err := v.ReadRaw(rel, vault.MaxReadSize)
	if err != nil {
		return removeNote(tx, v, rel)
	}
	hash := hashOf(raw, info)
	var oldHash string
	err = tx.QueryRow(`SELECT hash FROM notes WHERE path = ?`, rel).Scan(&oldHash)
	if err == nil && oldHash == hash {
		// Only the time changed, for example after a fresh git checkout.
		_, err = tx.Exec(`UPDATE notes SET mtime = ?, size = ? WHERE path = ?`, info.ModTime().UnixNano(), info.Size(), rel)
		return err
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := removeNote(tx, v, rel); err != nil {
		return err
	}
	if rel == v.MemoryFile() {
		if err := insertMemoryLines(tx, raw); err != nil {
			return err
		}
	}
	return insertNote(tx, rel, raw, info, hash)
}

// insertMemoryLines stores the memory note line by line, so that memory.recall
// can return single memories instead of the whole file.
func insertMemoryLines(tx *sql.Tx, raw []byte) error {
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO memory_fts (line) VALUES (?)`, line); err != nil {
			return err
		}
	}
	return nil
}

// hashOf hashes the content. Oversized notes are not read, so their size and
// time stand in for it.
func hashOf(raw []byte, info fs.FileInfo) string {
	if raw == nil {
		return fmt.Sprintf("big:%d:%d", info.Size(), info.ModTime().UnixNano())
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func insertNote(tx *sql.Tx, rel string, raw []byte, info fs.FileInfo, hash string) error {
	parsed := vault.Parse(rel, raw)
	tags, err := json.Marshal(parsed.Tags)
	if err != nil {
		return err
	}
	result, err := tx.Exec(`INSERT INTO notes (path, title, tags, mtime, size, hash) VALUES (?, ?, ?, ?, ?, ?)`,
		rel, parsed.Title, string(tags), info.ModTime().UnixNano(), info.Size(), hash)
	if err != nil {
		return err
	}
	rowid, err := result.LastInsertId()
	if err != nil {
		return err
	}
	// The search table shares rowids with notes. Deleting by rowid is instant,
	// while a lookup by path would scan the whole search table.
	_, err = tx.Exec(`INSERT INTO notes_fts (rowid, path, title, tags, body) VALUES (?, ?, ?, ?, ?)`,
		rowid, rel, parsed.Title, strings.Join(parsed.Tags, " "), parsed.Body)
	if err != nil {
		return err
	}
	for _, link := range parsed.Links {
		if _, err := tx.Exec(`INSERT INTO links (src, dst, line) VALUES (?, ?, ?)`, rel, vault.LinkKey(link.Target), link.Line); err != nil {
			return err
		}
	}
	return nil
}

func removeNote(tx *sql.Tx, v *vault.Vault, rel string) error {
	if rel == v.MemoryFile() {
		if _, err := tx.Exec(`DELETE FROM memory_fts`); err != nil {
			return err
		}
	}
	var rowid int64
	err := tx.QueryRow(`SELECT rowid FROM notes WHERE path = ?`, rel).Scan(&rowid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM notes_fts WHERE rowid = ?`, rowid); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM links WHERE src = ?`, rel); err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM notes WHERE rowid = ?`, rowid)
	return err
}

// linkCandidates lists every link key that could point at a note: its full
// path, and each shorter path ending down to the bare file name.
func linkCandidates(rel string) []string {
	key := vault.LinkKey(rel)
	out := []string{key}
	for {
		_, rest, found := strings.Cut(key, "/")
		if !found {
			return out
		}
		out = append(out, rest)
		key = rest
	}
}
