// Package index keeps a SQLite full-text index of the vault up to date.
package index

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	_ "modernc.org/sqlite" // the database/sql driver

	"github.com/hortensia/vaultd/internal/vault"
)

const schemaVersion = 1

const schema = `
CREATE TABLE notes (path TEXT PRIMARY KEY, title TEXT, tags TEXT, mtime INTEGER, size INTEGER, hash TEXT);
CREATE VIRTUAL TABLE notes_fts USING fts5(path UNINDEXED, title, tags, body, tokenize = 'unicode61 remove_diacritics 2');
CREATE TABLE links (src TEXT, dst TEXT, line TEXT);
CREATE INDEX links_src ON links(src);
CREATE INDEX links_dst ON links(dst);
`

// Index is the search index of one vault. It is safe for concurrent use.
type Index struct {
	db     *sql.DB
	vaults atomic.Pointer[vault.Vault]

	writeMu sync.Mutex // one writer at a time inside this process
	syncMu  sync.Mutex // one full sync at a time

	indexing atomic.Bool
	done     atomic.Int64
	total    atomic.Int64
}

// Open opens or creates the index database at dbPath. It fails with a clear
// error if this SQLite build lacks FTS5.
func Open(dbPath string, v *vault.Vault) (*Index, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create data folder: %w", err)
	}
	db, err := sql.Open("sqlite", dsn(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open index: %w", err)
	}
	if err := prepare(db); err != nil {
		db.Close()
		return nil, err
	}
	ix := &Index{db: db}
	ix.vaults.Store(v)
	return ix, nil
}

// dsn turns on write-ahead logging, so searches keep working during indexing,
// and waits on a lock held by another vaultd process instead of failing.
func dsn(dbPath string) string {
	query := url.Values{}
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "busy_timeout(10000)")
	query.Add("_pragma", "synchronous(NORMAL)")
	query.Set("_txlock", "immediate")
	return "file:" + filepath.ToSlash(dbPath) + "?" + query.Encode()
}

func prepare(db *sql.DB) error {
	if _, err := db.Exec(`CREATE VIRTUAL TABLE temp.fts5_check USING fts5(x); DROP TABLE temp.fts5_check`); err != nil {
		return fmt.Errorf("this SQLite build has no FTS5 full-text search: %w", err)
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read index version: %w", err)
	}
	if version == schemaVersion {
		return nil
	}
	return createSchema(db)
}

// createSchema replaces any older index. The index holds nothing that cannot
// be rebuilt from the vault, so there is nothing to migrate.
func createSchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("create index: %w", err)
	}
	defer tx.Rollback()
	statements := "DROP TABLE IF EXISTS notes; DROP TABLE IF EXISTS notes_fts; DROP TABLE IF EXISTS links;" +
		schema + fmt.Sprintf("PRAGMA user_version = %d;", schemaVersion)
	if _, err := tx.Exec(statements); err != nil {
		return fmt.Errorf("create index: %w", err)
	}
	return tx.Commit()
}

// SetVault points the index at a vault with other settings. Stop the watcher
// first, then start it again and let it sync.
func (ix *Index) SetVault(v *vault.Vault) { ix.vaults.Store(v) }

func (ix *Index) vault() *vault.Vault { return ix.vaults.Load() }

// Close closes the database.
func (ix *Index) Close() error { return ix.db.Close() }

// State describes the index's progress.
type State struct {
	Indexing bool  `json:"indexing"`
	Done     int64 `json:"done"`
	Total    int64 `json:"total"`
	Notes    int64 `json:"notes"`
}

// State reports the index's current progress.
func (ix *Index) State() State {
	state := State{Indexing: ix.indexing.Load(), Done: ix.done.Load(), Total: ix.total.Load()}
	// A failure here only makes a progress display show zero.
	_ = ix.db.QueryRow(`SELECT count(*) FROM notes`).Scan(&state.Notes)
	return state
}

// write runs fn in one transaction.
func (ix *Index) write(ctx context.Context, fn func(*sql.Tx) error) error {
	ix.writeMu.Lock()
	defer ix.writeMu.Unlock()
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
