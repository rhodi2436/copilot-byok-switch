//go:build darwin

package winenv

import (
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
