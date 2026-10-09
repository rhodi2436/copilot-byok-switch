//go:build darwin

package winenv

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderLaunchAgent(t *testing.T) {
	data, err := renderLaunchAgent("/Applications/Cops & Tools.app/Contents/MacOS/cops", "tray --autostart")
	if err != nil {
		t.Fatalf("render LaunchAgent: %v", err)
	}
	plist := string(data)
	for _, want := range []string{
		"<!DOCTYPE plist",
		"com.cops.tray",
		"/Applications/Cops &amp; Tools.app/Contents/MacOS/cops",
		"<string>tray</string>",
		"<string>--autostart</string>",
		"<key>RunAtLoad</key>",
		"<true></true>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("LaunchAgent plist missing %q:\n%s", want, plist)
		}
	}
	path := filepath.Join(t.TempDir(), "com.cops.tray.plist")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write LaunchAgent plist: %v", err)
	}
	out, err := exec.Command("/usr/bin/plutil", "-lint", path).CombinedOutput()
	if err != nil {
		t.Fatalf("LaunchAgent plist is invalid: %v: %s", err, out)
	}
}

// 通过替换 zshrcPath 指向临时文件，验证 darwin 环境变量三件套全链路。
func TestDarwinUserEnvRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".zshrc")
	orig := zshrcPath
	zshrcPath = func() string { return path }
	defer func() { zshrcPath = orig }()

	if err := SetUserEnv("COPILOT_TEST_A", "hello"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := SetUserEnv("COPILOT_TEST_B", "http://127.0.0.1:8317/v1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, ok := GetUserEnv("COPILOT_TEST_A"); !ok || v != "hello" {
		t.Fatalf("Get A = %q, %v", v, ok)
	}

	if err := DeleteUserEnv("COPILOT_TEST_A"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := GetUserEnv("COPILOT_TEST_A"); ok {
		t.Fatal("A should be deleted")
	}
	if v, ok := GetUserEnv("COPILOT_TEST_B"); !ok || v != "http://127.0.0.1:8317/v1" {
		t.Fatalf("Get B after delete A = %q, %v", v, ok)
	}

	// 删光后整块消失
	if err := DeleteUserEnv("COPILOT_TEST_B"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, found := ReadShellBlock(path); found {
		t.Fatal("block should be removed when empty")
	}

	if !TraySupported() {
		t.Fatal("darwin 应启用托盘")
	}
}

func TestInjectUpgradeRestoresNewVariables(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".zshrc")
	orig := zshrcPath
	zshrcPath = func() string { return path }
	defer func() { zshrcPath = orig }()

	if err := SetUserEnv("no_proxy", "corp.example"); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "env-backup.json")
	if err := InjectEnv(backupPath, map[string]string{"COPILOT_MODEL": "cops-active"}); err != nil {
		t.Fatal(err)
	}
	if err := InjectEnv(backupPath, map[string]string{
		"COPILOT_MODEL": "cops-active",
		"NO_PROXY":      "corp.example,localhost,127.0.0.1,::1",
		"no_proxy":      "corp.example,localhost,127.0.0.1,::1",
	}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	var backup EnvBackup
	if err := json.Unmarshal(data, &backup); err != nil {
		t.Fatal(err)
	}
	if entry := backup.Vars["no_proxy"]; !entry.Exists || entry.Value != "corp.example" {
		t.Fatalf("extended backup no_proxy = %+v, want original corp.example", entry)
	}
	if entry := backup.Vars["NO_PROXY"]; entry.Exists {
		t.Fatalf("extended backup NO_PROXY = %+v, want originally absent", entry)
	}

	restored, err := RestoreEnv(backupPath)
	if err != nil || !restored {
		t.Fatalf("RestoreEnv = %v, %v", restored, err)
	}
	if value, ok := GetUserEnv("no_proxy"); !ok || value != "corp.example" {
		t.Fatalf("restored no_proxy = %q, %v; want corp.example", value, ok)
	}
	if _, ok := GetUserEnv("NO_PROXY"); ok {
		t.Fatal("NO_PROXY should be removed after restore")
	}
	if value, ok := GetUserEnv("COPILOT_MODEL"); ok {
		t.Fatalf("newly injected COPILOT_MODEL should be removed, got %q", value)
	}
}
