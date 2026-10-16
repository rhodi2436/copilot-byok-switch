// Package update 检查 GitHub 最新 release 并做轻量 semver 比较，
// 支撑 cops 的「新版本提示」（仅提示，不自动替换）。
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Repo GitHub 仓库（owner/name）。
const Repo = "rhodi2436/copilot-byok-switch"

// Release 最新版本摘要。
type Release struct {
	Version string `json:"version"` // tag 名，如 "v0.4.1"
	URL     string `json:"url"`     // release 页面
}

// CheckLatest 查询 GitHub latest release。匿名 API 有速率限制且可能
// 因网络不可达失败——调用方应静默降级，不影响主功能。
func CheckLatest(ctx context.Context) (*Release, error) {
	url := "https://api.github.com/repos/" + Repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "cops/"+Repo) // GitHub API 强制要求 UA
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api: %s", resp.Status)
	}
	var out struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.TagName == "" {
		return nil, fmt.Errorf("release 缺少 tag_name")
	}
	return &Release{Version: out.TagName, URL: out.HTMLURL}, nil
}

// Loop 周期检查：delay 后首查，此后每 interval 一次；
// 发现比 current 新的版本时回调 onUpdate（失败静默）。ctx 取消即退出。
func Loop(ctx context.Context, current string, delay, interval time.Duration, onUpdate func(*Release)) {
	go func() {
		t := time.NewTimer(delay)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if rel, err := CheckLatest(ctx); err == nil && IsNewer(current, rel.Version) {
				onUpdate(rel)
			}
			t.Reset(interval)
		}
	}()
}

// IsNewer 判断 latest（如 "v0.4.1"）是否比 current 新。
// 非 semver 字符串（如 "dev"、"dev-a1b2c3d"）按 0.0.0 处理，
// 因此开发构建总能收到正式版提示。
func IsNewer(current, latest string) bool {
	return compareVersions(latest, current) > 0
}

// compareVersions 按数字段比较两个版本（最多 4 段，缺段补 0）。
func compareVersions(a, b string) int {
	sa, sb := segments(a), segments(b)
	for i := range sa {
		switch {
		case sa[i] > sb[i]:
			return 1
		case sa[i] < sb[i]:
			return -1
		}
	}
	return 0
}

func segments(v string) [4]int64 {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.SplitN(v, ".", 4)
	var out [4]int64
	for i := range out {
		if i < len(parts) {
			out[i] = parseDigits(parts[i])
		}
	}
	return out
}

// parseDigits 取字段开头的连续数字（"4-beta"→4；"rc1"→0；""→0）。
func parseDigits(s string) int64 {
	end := strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' })
	if end == -1 {
		end = len(s)
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.ParseInt(s[:end], 10, 64)
	if err != nil {
		return 0
	}
	return n
}
