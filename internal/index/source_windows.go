package index

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"
)

const (
	windowsBufferBytes = 64 * 1024
	completionWaitMS   = 200
	windowsMask        = syscall.FILE_NOTIFY_CHANGE_FILE_NAME | syscall.FILE_NOTIFY_CHANGE_DIR_NAME |
		syscall.FILE_NOTIFY_CHANGE_SIZE | syscall.FILE_NOTIFY_CHANGE_LAST_WRITE
)

// windowsSource watches the whole tree through one handle on the vault root,
// using the system's subtree mode. fsnotify keeps a handle on each watched
// folder, and Windows refuses to rename a folder if a folder below it is open.
// fsnotify has a subtree mode, but it is not part of its public API.
type windowsSource struct {
	root   string
	handle syscall.Handle
	port   syscall.Handle
	events chan change
	errs   chan error

	stop     chan struct{}
	stopOnce sync.Once
	stopped  chan struct{}
}

func newSource(root string) (source, error) {
	name, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(name, syscall.FILE_LIST_DIRECTORY,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil,
		syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS|syscall.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, fmt.Errorf("open vault folder: %w", err)
	}
	port, err := syscall.CreateIoCompletionPort(handle, 0, 0, 0)
	if err != nil {
		syscall.CloseHandle(handle)
		return nil, fmt.Errorf("create completion port: %w", err)
	}
	s := &windowsSource{
		root: root, handle: handle, port: port,
		events: make(chan change), errs: make(chan error),
		stop: make(chan struct{}), stopped: make(chan struct{}),
	}
	go s.run()
	return s, nil
}

// run repeats one overlapped read. The wait has a timeout so that it notices
// stop, since a closed handle does not reliably wake a waiting thread.
func (s *windowsSource) run() {
	defer close(s.stopped)
	defer close(s.events)
	defer close(s.errs)
	buffer := make([]uint32, windowsBufferBytes/4) // the system needs DWORD alignment
	var overlapped syscall.Overlapped
	reading := false
	defer func() {
		if reading {
			// The system writes into buffer until the read has really ended.
			syscall.CancelIoEx(s.handle, &overlapped)
			var n uint32
			var key uint32
			var done *syscall.Overlapped
			_ = syscall.GetQueuedCompletionStatus(s.port, &n, &key, &done, completionWaitMS*5)
		}
		syscall.CloseHandle(s.handle)
		syscall.CloseHandle(s.port)
	}()
	for {
		if !reading {
			err := syscall.ReadDirectoryChanges(s.handle, (*byte)(unsafe.Pointer(&buffer[0])), windowsBufferBytes,
				true, windowsMask, nil, &overlapped, 0)
			if err != nil {
				s.report(fmt.Errorf("read changes: %w", err))
				return
			}
			reading = true
		}
		var n uint32
		var key uint32
		var done *syscall.Overlapped
		err := syscall.GetQueuedCompletionStatus(s.port, &n, &key, &done, completionWaitMS)
		select {
		case <-s.stop:
			return
		default:
		}
		if done == nil {
			continue // only the timeout
		}
		reading = false
		if err != nil {
			if !s.report(fmt.Errorf("read changes: %w", err)) {
				return
			}
			continue
		}
		if !s.deliver(buffer, n) {
			return
		}
	}
}

// deliver reports false if the source is stopping. A result of zero bytes is how
// the system says its buffer overflowed.
func (s *windowsSource) deliver(buffer []uint32, n uint32) bool {
	if n == 0 {
		return s.report(errOverflow)
	}
	for _, event := range parseNotifications(unsafe.Slice((*byte)(unsafe.Pointer(&buffer[0])), n)) {
		event.path = filepath.Join(s.root, event.path)
		select {
		case s.events <- event:
		case <-s.stop:
			return false
		}
	}
	return true
}

func (s *windowsSource) report(err error) bool {
	select {
	case s.errs <- err:
		return true
	case <-s.stop:
		return false
	}
}

// parseNotifications reads FILE_NOTIFY_INFORMATION records: next-entry offset,
// action and name length as 32-bit values, then the name in UTF-16.
func parseNotifications(data []byte) []change {
	const headerBytes = 12
	var out []change
	for offset := 0; offset+headerBytes <= len(data); {
		record := data[offset:]
		next := int(*(*uint32)(unsafe.Pointer(&record[0])))
		action := *(*uint32)(unsafe.Pointer(&record[4]))
		size := int(*(*uint32)(unsafe.Pointer(&record[8])))
		if headerBytes+size > len(record) {
			return out
		}
		name := unsafe.Slice((*uint16)(unsafe.Pointer(&record[headerBytes])), size/2)
		out = append(out, change{
			path:    syscall.UTF16ToString(name),
			created: action == syscall.FILE_ACTION_ADDED || action == syscall.FILE_ACTION_RENAMED_NEW_NAME,
		})
		if next == 0 {
			return out
		}
		offset += next
	}
	return out
}

func (s *windowsSource) changes() <-chan change { return s.events }

func (s *windowsSource) failures() <-chan error { return s.errs }

// addTree has nothing to do: the subtree watch already covers new folders.
func (s *windowsSource) addTree(treeWalker, string, *slog.Logger) {}

func (s *windowsSource) close() {
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.stopped
}
