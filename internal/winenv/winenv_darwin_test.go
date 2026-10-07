//go:build darwin

package winenv

import (
	"path/filepath"
	"testing"
)

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

	if TraySupported() {
		t.Fatal("darwin 第一阶段 TraySupported 应为 false")
	}
}
