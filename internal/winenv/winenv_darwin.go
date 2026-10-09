//go:build darwin

// Package winenv 处理系统集成；本文件为 macOS 实现：
// 环境变量通过 ~/.zshrc 标记块注入（Copilot CLI 在终端运行，新开终端即生效），
// 退出恢复 = 删除标记块；开机自启通过用户级 LaunchAgent 管理。
package winenv

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const launchAgentLabel = "com.cops.tray"

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

type launchAgentPlist struct {
	XMLName xml.Name        `xml:"plist"`
	Version string          `xml:"version,attr"`
	Dict    launchAgentDict `xml:"dict"`
}

type launchAgentDict struct {
	ProgramArguments []string
}

func (v launchAgentDict) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	start.Name.Local = "dict"
	if err := e.EncodeToken(start); err != nil {
		return err
	}
	if err := encodePlistEntry(e, "Label", launchAgentLabel); err != nil {
		return err
	}
	if err := encodePlistKey(e, "ProgramArguments"); err != nil {
		return err
	}
	array := xml.StartElement{Name: xml.Name{Local: "array"}}
	if err := e.EncodeToken(array); err != nil {
		return err
	}
	for _, arg := range v.ProgramArguments {
		if err := e.EncodeElement(arg, xml.StartElement{Name: xml.Name{Local: "string"}}); err != nil {
			return err
		}
	}
	if err := e.EncodeToken(array.End()); err != nil {
		return err
	}
	if err := encodePlistKey(e, "RunAtLoad"); err != nil {
		return err
	}
	if err := e.EncodeToken(xml.StartElement{Name: xml.Name{Local: "true"}}); err != nil {
		return err
	}
	if err := e.EncodeToken(xml.EndElement{Name: xml.Name{Local: "true"}}); err != nil {
		return err
	}
	return e.EncodeToken(start.End())
}

func encodePlistKey(e *xml.Encoder, key string) error {
	return e.EncodeElement(key, xml.StartElement{Name: xml.Name{Local: "key"}})
}

func encodePlistEntry(e *xml.Encoder, key, value string) error {
	if err := encodePlistKey(e, key); err != nil {
		return err
	}
	return e.EncodeElement(value, xml.StartElement{Name: xml.Name{Local: "string"}})
}

func renderLaunchAgent(exePath, args string) ([]byte, error) {
	exePath, err := filepath.Abs(exePath)
	if err != nil {
		return nil, fmt.Errorf("解析 cops 可执行文件路径失败: %w", err)
	}
	programArgs := append([]string{exePath}, strings.Fields(args)...)
	plist, err := xml.MarshalIndent(launchAgentPlist{
		Version: "1.0",
		Dict:    launchAgentDict{ProgramArguments: programArgs},
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("生成 LaunchAgent 配置失败: %w", err)
	}
	content := append([]byte(xml.Header+"<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n"), plist...)
	return append(content, '\n'), nil
}

func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("读取用户主目录失败: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist"), nil
}

func launchctl(args ...string) error {
	out, err := exec.Command("/bin/launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s 失败: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func launchAgentTarget() string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), launchAgentLabel)
}

// SetAutostart 安装并加载用户级 LaunchAgent，在图形会话登录时启动托盘。
func SetAutostart(exePath, args string) error {
	content, err := renderLaunchAgent(exePath, args)
	if err != nil {
		return err
	}
	path, err := launchAgentPath()
	if err != nil {
		return err
	}
	if err := atomicWrite(path, string(content)); err != nil {
		return fmt.Errorf("写入 LaunchAgent 配置失败: %w", err)
	}

	if err := launchctl("print", launchAgentTarget()); err == nil {
		if err := launchctl("bootout", launchAgentTarget()); err != nil {
			return err
		}
	}
	if err := launchctl("bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), path); err != nil {
		return err
	}
	return nil
}

// RemoveAutostart 卸载用户级 LaunchAgent 并删除其配置文件。
func RemoveAutostart() error {
	path, err := launchAgentPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("检查 LaunchAgent 配置失败: %w", err)
	}
	if err := launchctl("print", launchAgentTarget()); err == nil {
		if err := launchctl("bootout", launchAgentTarget()); err != nil {
			return err
		}
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除 LaunchAgent 配置失败: %w", err)
	}
	return nil
}

// GetAutostart 返回已安装的 LaunchAgent 配置路径。
func GetAutostart() (string, bool) {
	path, err := launchAgentPath()
	if err != nil {
		return "", false
	}
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	return path, true
}

// FreeConsole macOS 无控制台黑窗问题，no-op。
func FreeConsole() {}

// SoleConsole 无控制台概念，恒 false（不会触发重启分离逻辑）。
func SoleConsole() bool { return false }

// RelaunchDetached 无需重启分离，直接不支持（调用方在 darwin 不会走到）。
func RelaunchDetached(args ...string) error { return ErrUnsupported }

// TraySupported systray 在 macOS 通过 cgo 使用原生菜单栏。
func TraySupported() bool { return true }
