//go:build windows

package winenv

import (
	"path/filepath"
	"testing"
)

// 使用测试前缀变量名，避免触碰真实 COPILOT_* 值；
// 注册表写入仅限 HKCU\Environment 下的 COPS_TEST_* 键并在测试中清理。
func TestInjectRestoreRoundtrip(t *testing.T) {
	if err := SetUserEnv("COPS_TEST_PROBE", "capability"); err != nil {
		t.Skipf("平台不支持注册表操作: %v", err)
	}
	_ = DeleteUserEnv("COPS_TEST_PROBE")

	nameA, nameB := "COPS_TEST_A", "COPS_TEST_B"
	backup := filepath.Join(t.TempDir(), "env-backup.json")

	// 清理残留
	_ = DeleteUserEnv(nameA)
	_ = DeleteUserEnv(nameB)
	_ = RemoveFile(backup)

	// 前置状态：A 有原值，B 不存在
	if err := SetUserEnv(nameA, "orig-a"); err != nil {
		t.Fatal(err)
	}

	// 注入
	if err := InjectEnv(backup, map[string]string{nameA: "new-a", nameB: "new-b"}); err != nil {
		t.Fatal(err)
	}
	if !IsInjected(backup) {
		t.Fatal("注入后应存在备份文件")
	}
	if v, ok := GetUserEnv(nameA); !ok || v != "new-a" {
		t.Fatalf("A = %q %v, want new-a", v, ok)
	}
	if v, ok := GetUserEnv(nameB); !ok || v != "new-b" {
		t.Fatalf("B = %q %v, want new-b", v, ok)
	}
	b, err := ReadBackup(backup)
	if err != nil {
		t.Fatal(err)
	}
	if ea, ok := b.Vars[nameA]; !ok || !ea.Exists || ea.Value != "orig-a" {
		t.Fatalf("备份 A = %+v, want orig-a/exists", ea)
	}
	if eb, ok := b.Vars[nameB]; !ok || eb.Exists {
		t.Fatalf("备份 B = %+v, want exists=false", eb)
	}

	// 幂等：二次注入不覆盖最早备份
	if err := SetUserEnv(nameA, "mutated"); err != nil {
		t.Fatal(err)
	}
	if err := InjectEnv(backup, map[string]string{nameA: "new-a2"}); err != nil {
		t.Fatal(err)
	}
	b2, _ := ReadBackup(backup)
	if ea := b2.Vars[nameA]; ea.Value != "orig-a" {
		t.Fatalf("二次注入后备份 A = %q, want 保留 orig-a", ea.Value)
	}

	// 恢复
	restored, err := RestoreEnv(backup)
	if err != nil || !restored {
		t.Fatalf("RestoreEnv = %v, %v", restored, err)
	}
	if v, ok := GetUserEnv(nameA); !ok || v != "orig-a" {
		t.Fatalf("恢复后 A = %q %v, want orig-a", v, ok)
	}
	if _, ok := GetUserEnv(nameB); ok {
		t.Fatal("恢复后 B 应被删除")
	}
	if IsInjected(backup) {
		t.Fatal("恢复后备份文件应被删除")
	}

	// 未注入时恢复是 no-op
	restored, err = RestoreEnv(backup)
	if err != nil || restored {
		t.Fatalf("二次 RestoreEnv = %v, %v, want false nil", restored, err)
	}

	// 清理
	_ = DeleteUserEnv(nameA)
	_ = DeleteUserEnv(nameB)
}

// 回归测试：备份目录不存在时（全新环境首次 cops tray）应自动创建。
func TestInjectCreatesBackupDir(t *testing.T) {
	if err := SetUserEnv("COPS_TEST_PROBE", "capability"); err != nil {
		t.Skipf("平台不支持注册表操作: %v", err)
	}
	_ = DeleteUserEnv("COPS_TEST_PROBE")

	root := t.TempDir()
	backup := filepath.Join(root, "nested", "home", ".cops", "env-backup.json")
	if err := InjectEnv(backup, map[string]string{"COPS_TEST_DIR": "v"}); err != nil {
		t.Fatalf("全新目录注入失败: %v", err)
	}
	if !IsInjected(backup) {
		t.Fatal("备份文件未创建")
	}
	restored, err := RestoreEnv(backup)
	if err != nil || !restored {
		t.Fatalf("RestoreEnv = %v, %v", restored, err)
	}
	_ = DeleteUserEnv("COPS_TEST_DIR")
}

func RemoveFile(path string) error {
	return removeFile(path)
}

// SyncInjectedVar 语义：value>0 写入并补备份（保留最早原值）；
// value<=0 仅当备份含该变量（cops 注入过）才恢复原值/删除，否则不碰用户值。
func TestSyncInjectedVarLifecycle(t *testing.T) {
	if err := SetUserEnv("COPS_TEST_PROBE", "capability"); err != nil {
		t.Skipf("平台不支持注册表操作: %v", err)
	}
	_ = DeleteUserEnv("COPS_TEST_PROBE")

	name := "COPS_TEST_MP"
	backup := filepath.Join(t.TempDir(), "env-backup.json")
	_ = DeleteUserEnv(name)
	_ = RemoveFile(backup)

	// 前置：变量有用户原值；主体变量已注入（模拟 v1.0.0 已注入机器补条件变量）。
	if err := SetUserEnv(name, "user-val"); err != nil {
		t.Fatal(err)
	}
	if err := InjectEnv(backup, map[string]string{"COPS_TEST_MAIN": "v"}); err != nil {
		t.Fatal(err)
	}

	// sync >0：写入并补备份原值。
	if err := SyncInjectedVar(backup, name, 200000); err != nil {
		t.Fatal(err)
	}
	if v, ok := GetUserEnv(name); !ok || v != "200000" {
		t.Fatalf("sync 后 = %q %v, want 200000", v, ok)
	}
	b, _ := ReadBackup(backup)
	if e, ok := b.Vars[name]; !ok || !e.Exists || e.Value != "user-val" {
		t.Fatalf("备份 = %+v, want user-val/exists", e)
	}

	// 二次 sync 换值：备份保留最早原值，现值更新。
	if err := SyncInjectedVar(backup, name, 300000); err != nil {
		t.Fatal(err)
	}
	if v, _ := GetUserEnv(name); v != "300000" {
		t.Fatalf("二次 sync 后 = %q, want 300000", v)
	}
	b2, _ := ReadBackup(backup)
	if e := b2.Vars[name]; e.Value != "user-val" {
		t.Fatalf("二次 sync 备份 = %q, want user-val", e.Value)
	}

	// sync <=0：恢复用户原值。
	if err := SyncInjectedVar(backup, name, 0); err != nil {
		t.Fatal(err)
	}
	if v, ok := GetUserEnv(name); !ok || v != "user-val" {
		t.Fatalf("清零后 = %q %v, want user-val", v, ok)
	}

	// 备份文件不存在（未注入）时 sync <=0 为 no-op，不碰用户值。
	_ = RemoveFile(backup)
	if err := SyncInjectedVar(backup, name, 0); err != nil {
		t.Fatal(err)
	}
	if v, ok := GetUserEnv(name); !ok || v != "user-val" {
		t.Fatalf("无备份清零 = %q %v, want user-val 保持", v, ok)
	}

	// 原值本不存在（备份 exists=false）：清零应删除变量。
	_ = RemoveFile(backup)
	if err := InjectEnv(backup, map[string]string{"COPS_TEST_MAIN": "v"}); err != nil {
		t.Fatal(err)
	}
	_ = DeleteUserEnv(name)
	if err := SyncInjectedVar(backup, name, 100); err != nil {
		t.Fatal(err)
	}
	if v, ok := GetUserEnv(name); !ok || v != "100" {
		t.Fatalf("首次 sync = %q %v, want 100", v, ok)
	}
	if err := SyncInjectedVar(backup, name, 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := GetUserEnv(name); ok {
		t.Fatal("原值不存在时清零应删除变量")
	}

	_, _ = RestoreEnv(backup)
	_ = DeleteUserEnv(name)
}
