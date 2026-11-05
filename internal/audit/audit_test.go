package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var testTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func openLog(t *testing.T, settings Settings) (*Logger, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	l, err := Open(dir, settings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l, dir
}

func readLines(t *testing.T, path string) []Entry {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var out []Entry
	scanner := bufio.NewScanner(file)
	scanner.Buffer(nil, 1<<24)
	for scanner.Scan() {
		var entry Entry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("bad line %q: %v", scanner.Text(), err)
		}
		out = append(out, entry)
	}
	return out
}

func TestRecord(t *testing.T) {
	l, dir := openLog(t, Settings{MaxSizeMB: 1, MaxFiles: 2, LogArguments: true})
	err := l.Record(Call{
		App: "a", Tool: "vault.read", Args: json.RawMessage(`{"path":"x.md"}`),
		Duration: 1500 * time.Millisecond, ResultBytes: 42,
	}, testTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Record(Call{App: "a", Tool: "t", Err: "broke"}, testTime); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, filepath.Join(dir, fileName))
	first := lines[0]
	if first.TS != "2026-01-02T03:04:05Z" || first.App != "a" || first.Tool != "vault.read" || !first.OK ||
		first.DurationMS != 1500 || first.ResultBytes != 42 || first.ArgsBytes != 15 {
		t.Errorf("got %+v", first)
	}
	if first.Args.(map[string]any)["path"] != "x.md" {
		t.Errorf("args %v", first.Args)
	}
	if lines[1].OK || lines[1].Error != "broke" {
		t.Errorf("got %+v", lines[1])
	}
}

func TestLogArgumentsOffKeepsOnlySizes(t *testing.T) {
	l, dir := openLog(t, Settings{MaxSizeMB: 1, MaxFiles: 2})
	secret := `{"text":"very private"}`
	if err := l.Record(Call{App: "a", Tool: "t", Args: json.RawMessage(secret)}, testTime); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private") {
		t.Fatalf("arguments leaked: %s", raw)
	}
	if got := readLines(t, filepath.Join(dir, fileName))[0]; got.ArgsBytes != len(secret) {
		t.Errorf("got %+v", got)
	}
}

func TestTruncate(t *testing.T) {
	long := strings.Repeat("é", 501)
	exact := strings.Repeat("x", 500)
	got := Truncate(map[string]any{
		"long": long, "exact": exact, "n": 1.5, "b": true, "none": nil,
		"list":   []any{long, 2.0},
		"nested": map[string]any{"deep": []any{map[string]any{"s": long}}},
		long:     "key",
	}).(map[string]any)
	want := strings.Repeat("é", 500) + "…"
	if got["long"] != want {
		t.Errorf("long: %q", got["long"])
	}
	if got["exact"] != exact {
		t.Error("exactly 500 stays whole")
	}
	if got["n"] != 1.5 || got["b"] != true || got["none"] != nil {
		t.Errorf("other types: %v", got)
	}
	if got["list"].([]any)[0] != want {
		t.Error("list")
	}
	deep := got["nested"].(map[string]any)["deep"].([]any)[0].(map[string]any)["s"]
	if deep != want {
		t.Error("nested")
	}
	if got[want] != "key" {
		t.Error("keys are truncated too")
	}
}

func TestRecordTruncatesArgsAndErrors(t *testing.T) {
	l, dir := openLog(t, Settings{MaxSizeMB: 1, MaxFiles: 2, LogArguments: true})
	long := strings.Repeat("x", 2000)
	args, _ := json.Marshal(map[string]string{"text": long})
	if err := l.Record(Call{App: "a", Tool: "t", Args: args, Err: long}, testTime); err != nil {
		t.Fatal(err)
	}
	if err := l.Record(Call{App: "a", Tool: "t", Args: json.RawMessage(`not json ` + long)}, testTime); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, filepath.Join(dir, fileName))
	if got := lines[0].Args.(map[string]any)["text"].(string); len([]rune(got)) != 501 {
		t.Errorf("args length %d", len(got))
	}
	if len([]rune(lines[0].Error)) != 501 {
		t.Errorf("error length %d", len(lines[0].Error))
	}
	if got := lines[1].Args.(string); len([]rune(got)) != 501 {
		t.Errorf("bad args length %d", len(got))
	}
}

func fillLog(t *testing.T, l *Logger, lines int) {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"text": strings.Repeat("x", 400)})
	for range lines {
		if err := l.Record(Call{App: "a", Tool: "t", Args: args}, testTime); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRotationKeepsMaxFiles(t *testing.T) {
	l, dir := openLog(t, Settings{MaxSizeMB: 1, MaxFiles: 3, LogArguments: true})
	fillLog(t, l, 12000) // about 6 MB
	names, err := filepath.Glob(filepath.Join(dir, fileName+"*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 {
		t.Fatalf("got %v", names)
	}
	for _, name := range names {
		info, err := os.Stat(name)
		if err != nil || info.Size() > 1<<20 {
			t.Errorf("%s: %v %v", name, info.Size(), err)
		}
		readLines(t, name) // each file holds whole lines
	}
}

func TestRotationWithOneFileAndReopen(t *testing.T) {
	l, dir := openLog(t, Settings{MaxSizeMB: 1, MaxFiles: 1, LogArguments: true})
	fillLog(t, l, 5000)
	names, _ := filepath.Glob(filepath.Join(dir, fileName+"*"))
	if len(names) != 1 {
		t.Fatalf("got %v", names)
	}
	l.Close()
	reopened, err := Open(dir, Settings{MaxSizeMB: 1, MaxFiles: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if info, _ := os.Stat(names[0]); reopened.size != info.Size() || reopened.size == 0 {
		t.Errorf("size after reopen: %d", reopened.size)
	}
}

func TestLoweringMaxFilesDropsOldFiles(t *testing.T) {
	l, dir := openLog(t, Settings{MaxSizeMB: 1, MaxFiles: 5, LogArguments: true})
	fillLog(t, l, 12000)
	l.SetSettings(Settings{MaxSizeMB: 1, MaxFiles: 2, LogArguments: true})
	fillLog(t, l, 3000)
	if names, _ := filepath.Glob(filepath.Join(dir, fileName+"*")); len(names) != 2 {
		t.Fatalf("got %v", names)
	}
}

func TestConcurrentRecordsStayWhole(t *testing.T) {
	l, dir := openLog(t, Settings{MaxSizeMB: 1, MaxFiles: 2, LogArguments: true})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				if err := l.Record(Call{App: "a", Tool: "t"}, testTime); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if got := len(readLines(t, filepath.Join(dir, fileName))); got != 1000 {
		t.Fatalf("got %d", got)
	}
}
