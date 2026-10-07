//go:build darwin

// Package winenv 处理系统集成；本文件为 macOS 实现：
// 环境变量通过 ~/.zshrc 标记块注入（Copilot CLI 在终端运行，新开终端即生效），
// 退出恢复 = 删除标记块。托盘与开机自启（LaunchAgent）将在第二阶段提供。
package winenv

import (
	"os"
	"path/filepath"
)

// zshrcPath 返回标记块所在文件（测试可替换）。
var zshrcPath = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".zshrc"
	}
	return filepath.Join(home, ".zshrc")
}

// SetUserEnv 将 name=value 写入 ~/.zshrc 标记块（整块重写，幂等）。
func SetUserEnv(name, value string) error {
	vars, _ := ReadShellBlock(zshrcPath())
	if vars == nil {
		vars = map[string]string{}
	}
	vars[name] = value
	return UpdateShellBlock(zshrcPath(), vars)
}

// DeleteUserEnv 从标记块中移除 name；块空则整块删除。
func DeleteUserEnv(name string) error {
	vars, found := ReadShellBlock(zshrcPath())
	if !found {
		return nil
	}
	delete(vars, name)
	if len(vars) == 0 {
		_, err := RemoveShellBlock(zshrcPath())
		return err
	}
	return UpdateShellBlock(zshrcPath(), vars)
}

// GetUserEnv 读取标记块中的 name。
func GetUserEnv(name string) (string, bool) {
	vars, found := ReadShellBlock(zshrcPath())
	if !found {
		return "", false
	}
	v, ok := vars[name]
	return v, ok
}

// BroadcastSettingChange macOS 无需广播：新开终端自然读取 zshrc。
func BroadcastSettingChange() {}

// SetAutostart 开机自启（LaunchAgent）将在 macOS 托盘阶段提供。
func SetAutostart(exePath, args string) error { return ErrUnsupported }

// RemoveAutostart 同 SetAutostart，暂不支持。
func RemoveAutostart() error { return ErrUnsupported }

// GetAutostart 暂不支持，恒为未设置。
func GetAutostart() (string, bool) { return "", false }

// FreeConsole macOS 无控制台黑窗问题，no-op。
func FreeConsole() {}

// SoleConsole 无控制台概念，恒 false（不会触发重启分离逻辑）。
func SoleConsole() bool { return false }

// RelaunchDetached 无需重启分离，直接不支持（调用方在 darwin 不会走到）。
func RelaunchDetached(args ...string) error { return ErrUnsupported }

// TraySupported 托盘当前在 darwin 暂未启用（第二阶段）。
func TraySupported() bool { return false }
