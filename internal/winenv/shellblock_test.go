package winenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func zshrcForTest(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), ".zshrc")
}

func TestShellBlockRoundtrip(t *testing.T) {
	path := zshrcForTest(t)
	vars := map[string]string{"COPILOT_MODEL": "cops-active", "COPILOT_BASE": "http://127.0.0.1:8317"}

	if err := UpdateShellBlock(path, vars); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, found := ReadShellBlock(path)
	if !found || len(got) != 2 || got["COPILOT_MODEL"] != "cops-active" || got["COPILOT_BASE"] != "http://127.0.0.1:8317" {
		t.Fatalf("Read after update = %v, found=%v", got, found)
	}

	// 幂等：重复写同值不产生第二个块
	if err := UpdateShellBlock(path, vars); err != nil {
		t.Fatalf("Update again: %v", err)
	}
	if n := strings.Count(readFileOrEmpty(path), blockBegin); n != 1 {
		t.Fatalf("block count = %d, want 1", n)
	}

	// 更新单值：块内替换，其余内容不动
	updated := map[string]string{"COPILOT_MODEL": "cops-pro"}
	if err := UpdateShellBlock(path, updated); err != nil {
		t.Fatalf("Update changed: %v", err)
	}
	got, _ = ReadShellBlock(path)
	if len(got) != 1 || got["COPILOT_MODEL"] != "cops-pro" {
		t.Fatalf("Read after change = %v, want only cops-pro", got)
	}

	removed, err := RemoveShellBlock(path)
	if err != nil || !removed {
		t.Fatalf("Remove = %v, %v", removed, err)
	}
	if _, found := ReadShellBlock(path); found {
		t.Fatal("block should be gone after remove")
	}
	// 再删：无块返回 false 不报错
	removed, err = RemoveShellBlock(path)
	if err != nil || removed {
		t.Fatalf("Remove again = %v, %v, want false,nil", removed, err)
	}
}

func TestShellBlockPreservesSurroundingContent(t *testing.T) {
	path := zshrcForTest(t)
	pre := "# existing comment\nexport PATH=/usr/local/bin:$PATH"
	post := "eval \"$(starship init zsh)\""
	if err := os.WriteFile(path, []byte(pre+"\n\n"+post+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UpdateShellBlock(path, map[string]string{"A": "1"}); err != nil {
		t.Fatal(err)
	}
	content := readFileOrEmpty(path)
	if !strings.HasPrefix(content, pre+"\n") || !strings.Contains(content, post+"\n\n"+blockBegin) {
		t.Fatalf("surrounding content not preserved:\n%s", content)
	}
	if !strings.Contains(content, "export A='1'") {
		t.Fatalf("block line missing:\n%s", content)
	}
	// 去掉块后应还原为原始内容
	if _, err := RemoveShellBlock(path); err != nil {
		t.Fatal(err)
	}
	if got := readFileOrEmpty(path); got != pre+"\n\n"+post+"\n" {
		t.Fatalf("after remove = %q", got)
	}
}

func TestShellBlockQuoteEscaping(t *testing.T) {
	path := zshrcForTest(t)
	tricky := `it's a "tricky" $HOME \` + "`cmd`" + ` value`
	if err := UpdateShellBlock(path, map[string]string{"K": tricky}); err != nil {
		t.Fatal(err)
	}
	got, found := ReadShellBlock(path)
	if !found || got["K"] != tricky {
		t.Fatalf("roundtrip of tricky value failed: %q vs %q", got["K"], tricky)
	}
}

func TestShellBlockAppendToNoNewlineFile(t *testing.T) {
	path := zshrcForTest(t)
	if err := os.WriteFile(path, []byte("export EDITOR=vim"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UpdateShellBlock(path, map[string]string{"B": "2"}); err != nil {
		t.Fatal(err)
	}
	content := readFileOrEmpty(path)
	if !strings.HasPrefix(content, "export EDITOR=vim\n\n"+blockBegin) {
		t.Fatalf("expected blank line separation, got:\n%s", content)
	}
}

func TestShellBlockEmptyFileAndMissingFile(t *testing.T) {
	path := zshrcForTest(t) // 不存在
	if err := UpdateShellBlock(path, map[string]string{"C": "3"}); err != nil {
		t.Fatal(err)
	}
	if got := readFileOrEmpty(path); got != blockBegin+"\nexport C='3'\n"+blockEnd+"\n" {
		t.Fatalf("fresh file content = %q", got)
	}
	if _, found := ReadShellBlock(zshrcForTest(t)); found {
		t.Fatal("missing file should report not found")
	}
}

func TestShellBlockToleratesForeignLines(t *testing.T) {
	// 块内出现非 export 行或裸值时解析不崩溃、跳过/按字面接受
	path := zshrcForTest(t)
	content := blockBegin + "\n# comment\nexport X=9\nnot-an-export\n" + blockEnd + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, found := ReadShellBlock(path)
	if !found || got["X"] != "9" || len(got) != 1 {
		t.Fatalf("parse with noise = %v, found=%v", got, found)
	}
}
