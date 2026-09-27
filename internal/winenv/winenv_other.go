//go:build !windows

// Package winenv 处理 Windows 集成；本文件为非 Windows 平台的占位实现。
package winenv

import "errors"

var ErrUnsupported = errors.New("仅支持 Windows")

func SetUserEnv(name, value string) error   { return ErrUnsupported }
func DeleteUserEnv(name string) error       { return ErrUnsupported }
func GetUserEnv(name string) (string, bool) { return "", false }
func BroadcastSettingChange()               {}
func SetAutostart(exePath, args string) error {
	return ErrUnsupported
}
func RemoveAutostart() error       { return ErrUnsupported }
func GetAutostart() (string, bool) { return "", false }
func FreeConsole()                 {}
