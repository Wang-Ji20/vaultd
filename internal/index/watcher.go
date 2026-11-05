package index

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

const (
	debounceDelay = 500 * time.Millisecond
	debounceTick  = 50 * time.Millisecond
)

// errOverflow means the system dropped events, so nobody knows what changed.
var errOverflow = errors.New("too many file changes at once")

// change is one event from the system. Only the path counts: see process.
type change struct {
	path    string
	created bool
}

// source is the platform's way of hearing about file changes. Linux and macOS
// use fsnotify. Windows has its own, because watching each folder there stops
// the user from renaming any folder with a subfolder; see the design document.
type source interface {
	changes() <-chan change
	failures() <-chan error
	// addTree starts watching a folder and the folders below it.
	addTree(v treeWalker, rel string, logger *slog.Logger)
	close()
}

// treeWalker is the part of the vault a source needs.
type treeWalker interface {
	WalkFolders(start string, visit func(abs string) error) error
}

// Start runs an initial sync in the background and keeps the index current
// with a file watcher until ctx ends. Searches work from the moment it
// returns, over whatever is indexed so far. Wait for it with the returned
// channel.
func (ix *Index) Start(ctx context.Context, logger *slog.Logger) (<-chan struct{}, error) {
	events, err := newSource(ix.vault().Root())
	if err != nil {
		return nil, fmt.Errorf("start file watcher: %w", err)
	}
	w := &watch{ix: ix, source: events, log: logger, pending: map[string]pendingChange{}}
	events.addTree(ix.vault(), ".", logger) // before syncing, so no change falls between
	ix.indexing.Store(true)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer events.close()
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
	source  source
	log     *slog.Logger
	pending map[string]pendingChange // by absolute path
}

type pendingChange struct {
	due     time.Time
	created bool // created or moved in, at any point during the wait
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
		case event, ok := <-w.source.changes():
			if !ok {
				return
			}
			w.onEvent(event)
		case err, ok := <-w.source.failures():
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
func (w *watch) onEvent(event change) {
	rel, visible := w.ix.vault().Rel(event.path)
	if !visible {
		return
	}
	if event.created {
		if info, err := os.Lstat(event.path); err == nil && info.IsDir() {
			w.source.addTree(w.ix.vault(), rel, w.log)
		}
	}
	w.pending[event.path] = pendingChange{
		due:     time.Now().Add(debounceDelay),
		created: event.created || w.pending[event.path].created,
	}
}

// onError handles lost events. After an overflow nobody knows what changed, so
// the only safe answer is a full sync.
func (w *watch) onError(ctx context.Context, err error) {
	w.log.Warn("file watcher error", "error", err)
	if errors.Is(err, errOverflow) {
		go func() { w.logSyncError(w.ix.Sync(ctx)) }()
	}
}

func (w *watch) processDue(ctx context.Context, now time.Time) {
	for abs, item := range w.pending {
		if now.Before(item.due) {
			continue
		}
		delete(w.pending, abs)
		if err := w.process(ctx, abs, item.created); err != nil && !errors.Is(err, context.Canceled) {
			w.log.Error("reindex failed", "error", err)
		}
	}
}

// process looks at what the path is now rather than trusting the event. This
// handles every editor's save pattern alike: write in place, rename over, and
// delete then create.
func (w *watch) process(ctx context.Context, abs string, created bool) error {
	rel, ok := w.ix.vault().Rel(abs)
	if !ok {
		return nil
	}
	info, err := os.Lstat(abs)
	switch {
	case err != nil:
		return w.ix.RemoveTree(ctx, rel)
	case !info.IsDir():
		return w.ix.IndexPath(ctx, rel)
	case !created:
		return nil // a folder's time changes whenever a file in it does
	default:
		// A moved-in folder: its notes sent no events. Watching starts again here
		// as well as in onEvent. On Linux the old watch's "moved" event arrives
		// after the "created" one, and it removes the watch onEvent just added.
		w.source.addTree(w.ix.vault(), rel, w.log)
		return w.ix.Sync(ctx)
	}
}
