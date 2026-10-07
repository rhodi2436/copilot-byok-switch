//go:build !windows && !darwin

// Package winenv 处理系统集成；本文件为其余平台（如 Linux）的占位实现。
package winenv

func SetUserEnv(name, value string) error   { return ErrUnsupported }
func DeleteUserEnv(name string) error       { return ErrUnsupported }
func GetUserEnv(name string) (string, bool) { return "", false }
func BroadcastSettingChange()               {}
func SetAutostart(exePath, args string) error {
	return ErrUnsupported
}
func RemoveAutostart() error                { return ErrUnsupported }
func GetAutostart() (string, bool)          { return "", false }
func FreeConsole()                          {}
func SoleConsole() bool                     { return false }
func RelaunchDetached(args ...string) error { return ErrUnsupported }
func TraySupported() bool                   { return false }
