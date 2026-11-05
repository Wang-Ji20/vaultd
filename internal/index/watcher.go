package index

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	debounceDelay = 500 * time.Millisecond
	debounceTick  = 50 * time.Millisecond
)

// Start runs an initial sync in the background and keeps the index current
// with a file watcher until ctx ends. Searches work from the moment it
// returns, over whatever is indexed so far. Wait for it with the returned
// channel.
func (ix *Index) Start(ctx context.Context, logger *slog.Logger) (<-chan struct{}, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("start file watcher: %w", err)
	}
	w := &watch{ix: ix, fs: watcher, log: logger, pending: map[string]time.Time{}}
	w.addTree(".") // before syncing, so that no change falls between the two
	ix.indexing.Store(true)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer watcher.Close()
		synced := make(chan struct{})
		go func() {
			defer close(synced)
			w.logSyncError(ix.Sync(ctx))
		}()
		w.run(ctx)
		<-synced
	}()
	return done, nil
}

// watch is owned by one goroutine, so its fields need no lock.
type watch struct {
	ix      *Index
	fs      *fsnotify.Watcher
	log     *slog.Logger
	pending map[string]time.Time // absolute path -> when to process it
}

func (w *watch) logSyncError(err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		w.log.Error("index sync failed", "error", err)
	}
}

func (w *watch) run(ctx context.Context) {
	ticker := time.NewTicker(debounceTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.fs.Events:
			if !ok {
				return
			}
			w.onEvent(event)
		case err, ok := <-w.fs.Errors:
			if !ok {
				return
			}
			w.onError(ctx, err)
		case now := <-ticker.C:
			w.processDue(ctx, now)
		}
	}
}

// onEvent records the path for later. A new folder is watched at once, so that
// files created in it straight away are not missed.
func (w *watch) onEvent(event fsnotify.Event) {
	if _, visible := w.ix.vault().Rel(event.Name); !visible {
		return
	}
	if event.Has(fsnotify.Create) {
		if info, err := os.Lstat(event.Name); err == nil && info.IsDir() {
			if rel, ok := w.ix.vault().Rel(event.Name); ok {
				w.addTree(rel)
			}
		}
	}
	w.pending[event.Name] = time.Now().Add(debounceDelay)
}

// onError handles lost events. After an overflow nobody knows what changed, so
// the only safe answer is a full sync.
func (w *watch) onError(ctx context.Context, err error) {
	w.log.Warn("file watcher error", "error", err)
	if errors.Is(err, fsnotify.ErrEventOverflow) {
		go func() { w.logSyncError(w.ix.Sync(ctx)) }()
	}
}

func (w *watch) processDue(ctx context.Context, now time.Time) {
	for abs, due := range w.pending {
		if now.Before(due) {
			continue
		}
		delete(w.pending, abs)
		if err := w.process(ctx, abs); err != nil && !errors.Is(err, context.Canceled) {
			w.log.Error("reindex failed", "error", err)
		}
	}
}

// process looks at what the path is now rather than trusting the event. This
// handles every editor's save pattern alike: write in place, rename over, and
// delete then create.
func (w *watch) process(ctx context.Context, abs string) error {
	rel, ok := w.ix.vault().Rel(abs)
	if !ok {
		return nil
	}
	info, err := os.Lstat(abs)
	switch {
	case err != nil:
		return w.ix.RemoveTree(ctx, rel)
	case info.IsDir():
		return w.ix.Sync(ctx) // a moved-in folder: its notes sent no events
	default:
		return w.ix.IndexPath(ctx, rel)
	}
}

// addTree watches a folder and the folders below it. Failure is only logged:
// the system's watch limit may be reached on huge vaults, and search still
// works, though it may lag until the next start.
func (w *watch) addTree(rel string) {
	err := w.ix.vault().WalkFolders(rel, func(abs string) error {
		if err := w.fs.Add(abs); err != nil {
			w.log.Warn("cannot watch folder; changes in it may not appear until restart", "error", err)
		}
		return nil
	})
	if err != nil {
		w.log.Warn("cannot walk folder", "error", err)
	}
}
