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

func RemoveFile(path string) error {
	return removeFile(path)
}
