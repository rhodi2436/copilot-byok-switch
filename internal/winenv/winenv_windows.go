//go:build windows

// Package winenv 处理 Windows 集成：用户环境变量写入（含 WM_SETTINGCHANGE 广播）
// 与 HKCU Run 开机自启。
package winenv

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	envKeyPath = `Environment`
	runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValName = "cops"
)

// SetUserEnv 写入 HKCU 用户环境变量。
func SetUserEnv(name, value string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, envKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("打开 HKCU\\Environment 失败: %w", err)
	}
	defer k.Close()
	return k.SetStringValue(name, value)
}

// DeleteUserEnv 删除用户环境变量。
func DeleteUserEnv(name string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, envKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("打开 HKCU\\Environment 失败: %w", err)
	}
	defer k.Close()
	if err := k.DeleteValue(name); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}

// GetUserEnv 读取用户环境变量。
func GetUserEnv(name string) (string, bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, envKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	if err != nil {
		return "", false
	}
	return v, true
}

// BroadcastSettingChange 广播环境变量变更，让资源管理器与新终端立即感知。
// 实测部分机器上 HWND_BROADCAST 会无视 uTimeout 无限阻塞，
// 因此放到后台线程执行并用看门狗兜底：最多等 3 秒，超时即放弃
// （此时注销重登或重启后仍会生效）。
func BroadcastSettingChange() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		user32 := syscall.NewLazyDLL("user32.dll")
		proc := user32.NewProc("SendMessageTimeoutW")
		const (
			hwndBroadcast   = 0xFFFF
			wmSettingChange = 0x001A
			smtoAbortIfHung = 0x0008
		)
		env, err := syscall.UTF16PtrFromString("Environment")
		if err != nil {
			return
		}
		proc.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)), smtoAbortIfHung, 2000, 0)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		// 放弃等待广播完成
	}
}

// SetAutostart 写入 HKCU Run 自启动项（最小化控制台方式启动指定子命令，
// 如 "tray --autostart"）。
func SetAutostart(exePath, args string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("打开 HKCU Run 失败: %w", err)
	}
	defer k.Close()
	cmd := fmt.Sprintf(`cmd /c start "" /min "%s" %s`, exePath, args)
	return k.SetStringValue(runValName, cmd)
}

// FreeConsole 分离控制台窗口（托盘模式隐藏黑窗用）。
func FreeConsole() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	kernel32.NewProc("FreeConsole").Call()
}

// SoleConsole 报告当前控制台是否只挂载了本进程——双击 exe / Start-Process
// 启动的典型特征（真实终端里 cmd/pwsh 也会挂载同一控制台）。
// 无控制台（已处于分离模式）返回 false，保证重启不会递归。
func SoleConsole() bool {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	var buf [1]uint32
	n, _, _ := kernel32.NewProc("GetConsoleProcessList").Call(
		uintptr(unsafe.Pointer(&buf[0])), 1)
	return n == 1
}

// RelaunchDetached 以 DETACHED_PROCESS 方式重新拉起自身（子进程完全无控制台）。
// 调用方应随即返回退出，使原控制台窗口干净关闭，避免双击启动时残留黑窗。
func RelaunchDetached(args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
	return cmd.Start()
}

// TraySupported 托盘在 Windows 已启用。
func TraySupported() bool { return true }

// RemoveAutostart 删除自启动项。
func RemoveAutostart() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(runValName); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}

// GetAutostart 读取自启动命令行（不存在返回空）。
func GetAutostart() (string, bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValName)
	if err != nil {
		return "", false
	}
	return v, true
}
