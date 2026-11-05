// Package audit keeps a JSON Lines record of every tool call, rotated by size.
package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	fileName    = "audit.jsonl"
	maxTextSize = 500
)

// Entry is one line of the log.
type Entry struct {
	TS          string `json:"ts"`
	App         string `json:"app"`
	Tool        string `json:"tool"`
	Args        any    `json:"args,omitempty"`
	ArgsBytes   int    `json:"args_bytes"`
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	DurationMS  int64  `json:"duration_ms"`
	ResultBytes int    `json:"result_bytes"`
}

// Settings are the parts of the audit config that can change while running.
type Settings struct {
	MaxSizeMB    int
	MaxFiles     int
	LogArguments bool
}

// Logger appends entries to the log file. It is safe for concurrent use.
type Logger struct {
	mu       sync.Mutex
	dir      string
	settings Settings
	file     *os.File
	size     int64
}

// Open opens or creates the log in dir.
func Open(dir string, settings Settings) (*Logger, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create audit folder: %w", err)
	}
	l := &Logger{dir: dir, settings: settings}
	if err := l.openFile(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Logger) openFile() error {
	// The log may hold note text and tool arguments, so it stays private.
	file, err := os.OpenFile(filepath.Join(l.dir, fileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return fmt.Errorf("open audit log: %w", err)
	}
	l.file, l.size = file, info.Size()
	return nil
}

// SetSettings applies new settings to later entries.
func (l *Logger) SetSettings(settings Settings) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.settings = settings
}

// Call describes a finished tool call.
type Call struct {
	App         string
	Tool        string
	Args        json.RawMessage
	Err         string
	Duration    time.Duration
	ResultBytes int
}

// Record writes one tool call. It applies the argument and truncation rules,
// so no caller can bypass them.
func (l *Logger) Record(call Call, now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := Entry{
		TS:          now.UTC().Format(time.RFC3339Nano),
		App:         call.App,
		Tool:        call.Tool,
		ArgsBytes:   len(call.Args),
		OK:          call.Err == "",
		Error:       truncateText(call.Err),
		DurationMS:  call.Duration.Milliseconds(),
		ResultBytes: call.ResultBytes,
	}
	if l.settings.LogArguments {
		entry.Args = decodeArgs(call.Args)
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode audit entry: %w", err)
	}
	return l.writeLine(append(line, '\n'))
}

func decodeArgs(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return truncateText(string(raw))
	}
	return Truncate(value)
}

// Truncate shortens every string inside a JSON value to 500 characters.
func Truncate(value any) any {
	switch v := value.(type) {
	case string:
		return truncateText(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = Truncate(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[truncateText(key)] = Truncate(item)
		}
		return out
	}
	return value
}

func truncateText(text string) string {
	if utf8.RuneCountInString(text) <= maxTextSize {
		return text
	}
	return string([]rune(text)[:maxTextSize]) + "…"
}

func (l *Logger) writeLine(line []byte) error {
	limit := int64(l.settings.MaxSizeMB) * 1 << 20
	if l.size > 0 && l.size+int64(len(line)) > limit {
		if err := l.rotate(); err != nil {
			return err
		}
	}
	n, err := l.file.Write(line)
	l.size += int64(n)
	if err != nil {
		return fmt.Errorf("write audit log: %w", err)
	}
	return nil
}

// rotate turns audit.jsonl into audit.jsonl.1, .1 into .2, and so on. MaxFiles
// counts all files, including the one being written.
func (l *Logger) rotate() error {
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("rotate audit log: %w", err)
	}
	if err := l.shiftFiles(); err != nil {
		return err
	}
	return l.openFile()
}

func (l *Logger) shiftFiles() error {
	base := filepath.Join(l.dir, fileName)
	for _, old := range rotatedFiles(l.dir) {
		if old.number >= l.settings.MaxFiles-1 {
			if err := os.Remove(old.path); err != nil {
				return fmt.Errorf("rotate audit log: %w", err)
			}
		}
	}
	for n := l.settings.MaxFiles - 2; n >= 1; n-- {
		from := fmt.Sprintf("%s.%d", base, n)
		err := os.Rename(from, fmt.Sprintf("%s.%d", base, n+1))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("rotate audit log: %w", err)
		}
	}
	if l.settings.MaxFiles == 1 {
		return os.Remove(base)
	}
	if err := os.Rename(base, base+".1"); err != nil {
		return fmt.Errorf("rotate audit log: %w", err)
	}
	return nil
}

type rotated struct {
	path   string
	number int
}

// rotatedFiles lists audit.jsonl.N files, oldest first.
func rotatedFiles(dir string) []rotated {
	paths, err := filepath.Glob(filepath.Join(dir, fileName+".*"))
	if err != nil {
		return nil
	}
	var out []rotated
	for _, path := range paths {
		var number int
		suffix := filepath.Base(path)[len(fileName)+1:]
		if _, err := fmt.Sscanf(suffix, "%d", &number); err == nil && number > 0 {
			out = append(out, rotated{path, number})
		}
	}
	return out
}

// Close closes the log file.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}
