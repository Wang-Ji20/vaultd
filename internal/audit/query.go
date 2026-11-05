package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// Filter selects entries. Zero values match everything.
type Filter struct {
	App   string
	Tool  string
	OK    *bool
	Since time.Time
	Until time.Time
	Text  string // case-insensitive, searched in the whole line
}

// Row is one entry together with its original line, for showing the full JSON.
type Row struct {
	Entry
	Raw string
}

// Page is one page of results, newest first.
type Page struct {
	Rows    []Row
	HasMore bool
}

// AppStats summarizes one app's calls.
type AppStats struct {
	LastSeen string
	Calls    int
}

// Query returns one page of matching entries, newest first. It holds the
// logger's lock: on Windows, a reader's open file would make rotation fail.
func (l *Logger) Query(filter Filter, offset, limit int) (Page, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	page := Page{}
	skipped := 0
	err := l.eachNewestFirst(func(row Row) bool {
		if !filter.matches(row) {
			return true
		}
		if skipped < offset {
			skipped++
			return true
		}
		if len(page.Rows) == limit {
			page.HasMore = true
			return false
		}
		page.Rows = append(page.Rows, row)
		return true
	})
	return page, err
}

// Stats returns call counts and last-seen times per app, across all files.
func (l *Logger) Stats() (map[string]AppStats, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	stats := map[string]AppStats{}
	err := l.eachNewestFirst(func(row Row) bool {
		current := stats[row.App]
		if current.LastSeen == "" {
			current.LastSeen = row.TS
		}
		current.Calls++
		stats[row.App] = current
		return true
	})
	return stats, err
}

func (f Filter) matches(row Row) bool {
	if f.App != "" && row.App != f.App {
		return false
	}
	if f.Tool != "" && row.Tool != f.Tool {
		return false
	}
	if f.OK != nil && row.OK != *f.OK {
		return false
	}
	if !f.Since.IsZero() || !f.Until.IsZero() {
		ts, err := time.Parse(time.RFC3339Nano, row.TS)
		if err != nil || (!f.Since.IsZero() && ts.Before(f.Since)) || (!f.Until.IsZero() && ts.After(f.Until)) {
			return false
		}
	}
	return f.Text == "" || strings.Contains(strings.ToLower(row.Raw), strings.ToLower(f.Text))
}

// eachNewestFirst visits every entry from newest to oldest until visit returns
// false. A file is small enough to load: rotation bounds it to max_size_mb.
func (l *Logger) eachNewestFirst(visit func(Row) bool) error {
	for _, path := range l.filesNewestFirst() {
		rows, err := readRows(path)
		if err != nil {
			return err
		}
		for _, row := range slices.Backward(rows) {
			if !visit(row) {
				return nil
			}
		}
	}
	return nil
}

func (l *Logger) filesNewestFirst() []string {
	paths := []string{filepath.Join(l.dir, fileName)}
	older := rotatedFiles(l.dir)
	sort.Slice(older, func(i, j int) bool { return older[i].number < older[j].number })
	for _, file := range older {
		paths = append(paths, file.path)
	}
	return paths
}

// readRows skips lines it cannot parse: a torn line must not hide the rest of
// the log.
func readRows(path string) ([]Row, error) {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read audit log: %w", err)
	}
	defer file.Close()
	var rows []Row
	scanner := bufio.NewScanner(file)
	scanner.Buffer(nil, 1<<24)
	for scanner.Scan() {
		var entry Entry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err == nil {
			rows = append(rows, Row{Entry: entry, Raw: scanner.Text()})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read audit log: %w", err)
	}
	return rows, nil
}
