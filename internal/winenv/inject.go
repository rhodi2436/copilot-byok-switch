// 注入/恢复 COPILOT_* 用户环境变量：写入前备份原值到 env-backup.json，
// 恢复时按备份写回（原本不存在则删除）。备份文件存在与否即注入状态。
package winenv

import (
	"encoding/json"
	"os"
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
// 防止二次启动覆盖原始状态），然后写入并广播。
func InjectEnv(backupPath string, vars map[string]string) error {
	if !IsInjected(backupPath) {
		b := EnvBackup{Vars: make(map[string]BackupEntry, len(vars))}
		for name := range vars {
			v, ok := GetUserEnv(name)
			b.Vars[name] = BackupEntry{Value: v, Exists: ok}
		}
		if err := writeBackup(backupPath, &b); err != nil {
			return err
		}
	}
	for k, v := range vars {
		if err := SetUserEnv(k, v); err != nil {
			return err
		}
	}
	BroadcastSettingChange()
	return nil
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

func writeBackup(backupPath string, b *EnvBackup) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	tmp := backupPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, backupPath)
}
