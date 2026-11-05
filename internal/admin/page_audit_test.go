package admin_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hortensia/vaultd/internal/audit"
)

func TestAuditPage(t *testing.T) {
	f := loggedIn(t, "")
	log := f.gateway.AuditLog()
	now := time.Now()
	for i := range 55 {
		call := audit.Call{App: "stdio", Tool: fmt.Sprintf("tool.%02d", i)}
		if i == 54 {
			call.Err = "it <b>broke</b>"
			call.Tool = "tool.bad"
		}
		if err := log.Record(call, now); err != nil {
			t.Fatal(err)
		}
	}
	old := audit.Call{App: "stdio", Tool: "tool.old"}
	if err := log.Record(old, now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, first := f.get(t, "/admin/audit")
	if strings.Count(first, "<details>") != 50 || !strings.Contains(first, "page=2") {
		t.Errorf("first page: %d rows", strings.Count(first, "<details>"))
	}
	if strings.Contains(first, "<b>broke") {
		t.Error("log content must be escaped")
	}
	_, second := f.get(t, "/admin/audit?page=2")
	if strings.Count(second, "<details>") != 6 || !strings.Contains(second, "page=1") {
		t.Errorf("second page: %d rows", strings.Count(second, "<details>"))
	}
	cases := map[string]int{
		"result=error":            1,
		"result=ok":               50,
		"tool=tool.old":           1,
		"text=BROKE":              1,
		"app=nobody":              0,
		"range=24h&tool=tool.old": 0,
		"page=bogus":              50,
	}
	for query, want := range cases {
		if _, body := f.get(t, "/admin/audit?"+query); strings.Count(body, "<details>") != want {
			t.Errorf("%s: %d rows, want %d", query, strings.Count(body, "<details>"), want)
		}
	}
}
