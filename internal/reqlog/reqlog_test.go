package reqlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogAndReadLast(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, Options{Enabled: true, MaxBodyKB: 1, RetainDays: 7, MaxFileMB: 20})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	big := strings.Repeat("x", 5000)
	l.Log(Entry{Provider: "p", Model: "m", Status: 200, RequestBody: big, ResponseBody: big})
	entries, err := ReadLast(filepath.Join(dir, CurrentFile), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if len(e.RequestBody) > 2048 {
		t.Fatalf("body not truncated: %d", len(e.RequestBody))
	}
	if !strings.HasSuffix(e.RequestBody, "...[截断]") {
		t.Fatal("truncation marker missing")
	}
}

func TestDisabled(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, Options{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	l.Log(Entry{Status: 200})
	if _, err := os.Stat(filepath.Join(dir, CurrentFile)); !os.IsNotExist(err) {
		t.Fatal("disabled logger should not create file")
	}
}

func TestEntryJSONFields(t *testing.T) {
	raw := `{"time":"2026-01-01 00:00:00.000","provider":"glm","model":"glm-4.7","method":"POST","path":"/v1/chat/completions","status":200,"durationMs":123,"stream":true,"promptTokens":10,"completionTokens":20}`
	var e Entry
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatal(err)
	}
	if e.Provider != "glm" || !e.Stream || e.PromptTokens != 10 {
		t.Fatalf("mismatch: %+v", e)
	}
}
