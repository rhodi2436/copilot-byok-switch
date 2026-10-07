package winenv

import (
	"errors"
	"os"
)

// ErrUnsupported 当前平台不支持该操作（供调用方优雅降级）。
var ErrUnsupported = errors.New("当前平台不支持该操作")

func removeFile(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
