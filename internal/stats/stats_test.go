package stats

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecordAndSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.json")
	tr := New(path)
	tr.Record("glm", "glm-4.7", 1000, 2000, 0.5, false)
	tr.Record("glm", "glm-4.7", 10, 20, 0.001, true)
	tr.Record("deepseek", "deepseek-chat", 500, 500, 0.01, false)
	if err := tr.Flush(); err != nil {
		t.Fatal(err)
	}

	// 重新加载，验证持久化
	tr2 := New(path)
	sum := tr2.Summary(7)
	if len(sum) != 2 {
		t.Fatalf("want 2 rows, got %d: %+v", len(sum), sum)
	}
	var glm Row
	for _, r := range sum {
		if r.Provider == "glm" {
			glm = r
		}
	}
	if glm.Requests != 2 || glm.Errors != 1 || glm.InputTokens != 1010 || glm.OutputTokens != 2020 {
		t.Fatalf("glm row mismatch: %+v", glm)
	}
	if glm.Cost < 0.5009 || glm.Cost > 0.5011 {
		t.Fatalf("cost = %v", glm.Cost)
	}
}

func TestDaysFilter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.json")
	tr := New(path)
	tr.Record("p", "m", 1, 1, 0, false)
	rows := tr.Rows(1)
	if len(rows) != 1 {
		t.Fatalf("want 1 row today, got %d", len(rows))
	}
	if len(tr.Rows(0)) != 0 {
		t.Fatal("days=0 should exclude today")
	}
	_ = os.Remove(path)
}
