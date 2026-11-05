//go:build !windows

package index

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"

	"github.com/fsnotify/fsnotify"
)

// fsnotifySource adds one watch per folder, as the system's API requires.
type fsnotifySource struct {
	watcher *fsnotify.Watcher
	events  chan change
	errs    chan error
}

func newSource(string) (source, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	s := &fsnotifySource{watcher: watcher, events: make(chan change), errs: make(chan error)}
	go s.forward()
	return s, nil
}

func (s *fsnotifySource) forward() {
	defer close(s.events)
	defer close(s.errs)
	for {
		select {
		case event, ok := <-s.watcher.Events:
			if !ok {
				return
			}
			s.events <- change{path: event.Name, created: event.Has(fsnotify.Create)}
		case err, ok := <-s.watcher.Errors:
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				err = errOverflow
			}
			s.errs <- err
		}
	}
}

func (s *fsnotifySource) changes() <-chan change { return s.events }

func (s *fsnotifySource) failures() <-chan error { return s.errs }

// addTree only logs a failure: the system's watch limit may be reached on huge
// vaults, and search still works, though it may lag until the next start.
func (s *fsnotifySource) addTree(v treeWalker, rel string, logger *slog.Logger) {
	s.dropStaleWatches()
	err := v.WalkFolders(rel, func(abs string) error {
		if err := s.watcher.Add(abs); err != nil {
			logger.Warn("cannot watch folder; changes in it may not appear until restart", "error", err)
		}
		return nil
	})
	if err != nil {
		logger.Warn("cannot walk folder", "error", err)
	}
}

// dropStaleWatches forgets folders that are no longer at their watched path. A
// folder below a renamed one keeps its watch, and adding it under the new name
// returns that same watch with the old name kept. Its events would then report
// the old path, and the file would look deleted. Removing the watch first makes
// the next add a fresh one.
func (s *fsnotifySource) dropStaleWatches() {
	for _, path := range s.watcher.WatchList() {
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			// A failure means the watch is already gone, which is what we want.
			_ = s.watcher.Remove(path)
		}
	}
}

func (s *fsnotifySource) close() { s.watcher.Close() }
