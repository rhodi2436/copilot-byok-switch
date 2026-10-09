// 注入/恢复 COPILOT_* 用户环境变量：写入前备份原值到 env-backup.json，
// 恢复时按备份写回（原本不存在则删除）。备份文件存在与否即注入状态。
package winenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// BackupEntry 单个变量的注入前状态。
type BackupEntry struct {
	Value  string `json:"value"`
	Exists bool   `json:"exists"`
}

// EnvBackup 备份文件内容。
type EnvBackup struct {
	Vars map[string]BackupEntry `json:"vars"`
}

// IsInjected 备份文件存在即处于注入状态。
func IsInjected(backupPath string) bool {
	_, err := os.Stat(backupPath)
	return err == nil
}

// InjectEnv 写入 vars 前先备份原值（幂等：已有备份则保留最早的原值，
// 防止二次启动覆盖原始状态），然后写入并广播。已有备份会补充新注入变量的原值，
// 以便升级后恢复新增的环境变量。
// 备份以 O_CREATE|O_EXCL 原子创建，避免并发调用覆盖最早的原值。
func InjectEnv(backupPath string, vars map[string]string) error {
	if err := createBackupIfAbsent(backupPath, vars); err != nil {
		return err
	}
	for k, v := range vars {
		if err := SetUserEnv(k, v); err != nil {
			return err
		}
	}
	BroadcastSettingChange()
	return nil
}

// createBackupIfAbsent 原子创建备份；已存在时仅补充缺失的变量。
// 注意：Windows 上句柄未关闭时无法删除文件（Go 打开不带 FILE_SHARE_DELETE），
// 因此失败路径必须先 Close 再 Remove，否则清理是无效操作。
func createBackupIfAbsent(backupPath string, vars map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(backupPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if os.IsExist(err) {
		return extendBackup(backupPath, vars)
	}
	if err != nil {
		return err
	}
	b := EnvBackup{Vars: make(map[string]BackupEntry, len(vars))}
	for name := range vars {
		v, ok := GetUserEnv(name)
		b.Vars[name] = BackupEntry{Value: v, Exists: ok}
	}
	abandon := func(writeErr error) error {
		_ = f.Close()
		_ = os.Remove(backupPath) // 写失败不得留下空/半成品备份（毒丸）
		return writeErr
	}
	data, err := json.MarshalIndent(&b, "", "  ")
	if err != nil {
		return abandon(err)
	}
	if _, err := f.Write(data); err != nil {
		return abandon(err)
	}
	return f.Close()
}

// extendBackup 为升级后新增注入的变量补充原值，保留已有备份项不变。
func extendBackup(backupPath string, vars map[string]string) error {
	var backup EnvBackup
	var data []byte
	var err error
	for attempt := 0; attempt < 50; attempt++ {
		data, err = os.ReadFile(backupPath)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, &backup); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	if backup.Vars == nil {
		backup.Vars = make(map[string]BackupEntry)
	}
	changed := false
	for name := range vars {
		if _, exists := backup.Vars[name]; exists {
			continue
		}
		value, exists := GetUserEnv(name)
		backup.Vars[name] = BackupEntry{Value: value, Exists: exists}
		changed = true
	}
	if !changed {
		return nil
	}
	data, err = json.MarshalIndent(&backup, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(backupPath), ".env-backup-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), backupPath)
}

// RestoreEnv 按备份还原变量并删除备份；无备份（未注入）时返回 false 且不做任何事。
func RestoreEnv(backupPath string) (bool, error) {
	data, err := os.ReadFile(backupPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var b EnvBackup
	if err := json.Unmarshal(data, &b); err != nil {
		// 损坏的备份：改名为 .corrupt 保留现场供人工排查，避免永久毒丸状态；
		// 改名也失败（如被占用）则退回删除。
		if rerr := os.Rename(backupPath, backupPath+".corrupt"); rerr != nil {
			_ = os.Remove(backupPath)
		}
		return false, err
	}
	for name, e := range b.Vars {
		if e.Exists {
			if err := SetUserEnv(name, e.Value); err != nil {
				return true, err
			}
		} else if err := DeleteUserEnv(name); err != nil {
			return true, err
		}
	}
	if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
		return true, err
	}
	BroadcastSettingChange()
	return true, nil
}

// ReadBackup 读取备份内容（doctor / 状态展示用）。
func ReadBackup(backupPath string) (*EnvBackup, error) {
	data, err := os.ReadFile(backupPath)
	if err != nil {
		return nil, err
	}
	var b EnvBackup
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, err
	}
	return &b, nil
}
