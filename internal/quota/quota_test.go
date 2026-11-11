package quota

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"cops/internal/config"
)

func TestWindowPercent(t *testing.T) {
	cases := []struct {
		w    Window
		want float64
	}{
		{Window{Type: "five_hour", Used: 95, Limit: 100}, 95},
		{Window{Type: "monthly", Used: 410000, Limit: 1000000}, 41},
		{Window{Type: "monthly", Used: 50, Limit: 0}, 0}, // 额度未知
		{Window{Used: 0, Limit: 100}, 0},
	}
	for _, c := range cases {
		if got := c.w.Percent(); got != c.want {
			t.Errorf("Window(%v).Percent() = %v, want %v", c.w, got, c.want)
		}
	}
}

func TestUsageBinding(t *testing.T) {
	u := &Usage{Windows: []Window{
		{Type: "five_hour", Used: 96, Limit: 100}, // 96%
		{Type: "monthly", Used: 410, Limit: 1000}, // 41%
		{Type: "weekly", Used: 10},                // 额度未知，跳过
	}}
	w, ok := u.Binding()
	if !ok || w.Type != "five_hour" || w.Percent() != 96 {
		t.Fatalf("Binding() = %+v, %v；want five_hour 96%%", w, ok)
	}
	if u.BindingPercent() != 96 {
		t.Errorf("BindingPercent() = %v, want 96", u.BindingPercent())
	}
	if s := u.FormatWindows(); s != "5小时 96.0% · 月 41.0% · 周 已用 10（额度未知）" {
		t.Errorf("FormatWindows() = %q", s)
	}

	empty := &Usage{}
	if _, ok := empty.Binding(); ok {
		t.Error("空窗口 Binding() 应返回 false")
	}
}

func TestDetectKind(t *testing.T) {
	cases := map[string]string{
		"https://open.bigmodel.cn/api/coding/paas/v4": "glm",
		"https://open.bigmodel.cn/api/paas/v4":        "glm",
		"https://api.z.ai/api/coding/paas/v4":         "glm",
		"https://api.deepseek.com/v1":                 "",
		"http://localhost:11434/v1":                   "",
	}
	for url, want := range cases {
		if got := DetectKind(url); got != want {
			t.Errorf("DetectKind(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestResolveKindExplicit(t *testing.T) {
	p := &config.Provider{Name: "x", BaseURL: "https://api.deepseek.com/v1"}
	if got := ResolveKind(p); got != "" {
		t.Errorf("未配置且无法探测时应为空， got %q", got)
	}
	p.Quota = &config.QuotaConfig{Kind: "glm"}
	if got := ResolveKind(p); got != "glm" {
		t.Errorf("显式 kind 优先：got %q", got)
	}
	p.Quota = &config.QuotaConfig{Kind: "none"}
	if got := ResolveKind(p); got != "none" {
		t.Errorf("none 应透传：got %q", got)
	}
}

func snap(used5h, limit5h, usedMon, limitMon float64, err error) *Snapshot {
	if err != nil {
		return &Snapshot{Err: err}
	}
	var u *Usage
	if limit5h > 0 || limitMon > 0 || used5h > 0 || usedMon > 0 {
		u = &Usage{Windows: []Window{}}
		if limit5h > 0 || used5h > 0 {
			u.Windows = append(u.Windows, Window{Type: "five_hour", Used: used5h, Limit: limit5h})
		}
		if limitMon > 0 || usedMon > 0 {
			u.Windows = append(u.Windows, Window{Type: "monthly", Used: usedMon, Limit: limitMon})
		}
	}
	return &Snapshot{Usage: u}
}

func TestDecide(t *testing.T) {
	// 常规：active 5h 窗口 96% ≥ 95%，第一候选 deepseek 未知用量（可用）→ 切换。
	snaps := map[string]*Snapshot{
		"glm":      snap(96, 100, 410, 1000, nil),
		"deepseek": snap(0, 0, 0, 0, nil), // 无用量数据 = 未知 = 可用
	}
	to, reason, ok := Decide("glm", []string{"deepseek", "glm2"}, snaps, 95)
	if !ok || to != "deepseek" {
		t.Fatalf("应切换到 deepseek：got %q %v %q", to, ok, reason)
	}
	if reason == "" {
		t.Error("切换原因不能为空")
	}

	// 顺序优先：第一候选也超阈值时跳到下一个。
	snaps["glm2"] = snap(98, 100, 0, 0, nil)
	to, _, ok = Decide("glm", []string{"glm2", "deepseek"}, snaps, 95)
	if !ok || to != "deepseek" {
		t.Fatalf("应跳过超阈值的 glm2 切到 deepseek：got %q %v", to, ok)
	}

	// 全部超阈值：不切换但给出原因。
	snaps["deepseek"] = snap(97, 100, 0, 0, nil)
	_, reason, ok = Decide("glm", []string{"glm2", "deepseek"}, snaps, 95)
	if ok || reason == "" {
		t.Fatalf("全部超阈值应 ok=false 且有原因：got %v %q", ok, reason)
	}

	// active 用量未知：不动作。
	_, _, ok = Decide("unknown-prov", []string{"deepseek"}, snaps, 95)
	if ok {
		t.Error("active 无快照时不应切换")
	}

	// active 查询失败：保守不动作。
	snaps["glm"] = snap(0, 0, 0, 0, errors.New("网络错误"))
	if _, _, ok = Decide("glm", []string{"deepseek"}, snaps, 95); ok {
		t.Error("active 查询失败时不应切换")
	}

	// 未达阈值：不动作。
	snaps["deepseek"] = snap(0, 0, 0, 0, nil)
	snaps["glm"] = snap(50, 100, 410, 1000, nil)
	if _, _, ok = Decide("glm", []string{"deepseek"}, snaps, 95); ok {
		t.Error("未达阈值不应切换")
	}

	// 约束最紧窗口判定：5h 40% 但月窗口 99% → 触发。
	snaps["glm"] = snap(40, 100, 99, 100, nil)
	to, reason, ok = Decide("glm", []string{"deepseek"}, snaps, 95)
	if !ok || to != "deepseek" {
		t.Fatalf("月窗口 99%% 应触发切换：got %q %v %q", to, ok, reason)
	}

	// 候选查询失败：跳过（状态不明）。
	snaps["deepseek"] = snap(0, 0, 0, 0, errors.New("超时"))
	snaps["glm"] = snap(96, 100, 0, 0, nil)
	to, _, ok = Decide("glm", []string{"deepseek"}, snaps, 95)
	if ok {
		t.Fatalf("候选查询失败不可作为目标：got %q", to)
	}
}

// fakeSource 注入假查询实现，验证 Manager 分发与缓存。
func TestManagerQueryDispatchAndCache(t *testing.T) {
	calls := 0
	prev, had := sources["fake"]
	sources["fake"] = func(ctx context.Context, hc *http.Client, p *config.Provider) (*Usage, error) {
		calls++
		return &Usage{Windows: []Window{{Type: "five_hour", Used: 1, Limit: 2}}}, nil
	}
	defer func() {
		if had {
			sources["fake"] = prev
		} else {
			delete(sources, "fake")
		}
	}()

	p := &config.Provider{Name: "fp", BaseURL: "https://x.example/v1", Quota: &config.QuotaConfig{Kind: "fake"}}
	m := NewManager(nil, time.Minute)

	u, err := m.Query(context.Background(), p, false)
	if err != nil || u == nil || u.BindingPercent() != 50 {
		t.Fatalf("Query = %+v, %v", u, err)
	}
	if u.Provider != "fp" || u.FetchedAt.IsZero() {
		t.Errorf("Query 应回填 provider/fetchedAt：%+v", u)
	}
	if _, err = m.Query(context.Background(), p, false); err != nil || calls != 1 {
		t.Errorf("缓存应命中（calls=%d, err=%v）", calls, err)
	}
	if _, err = m.Query(context.Background(), p, true); err != nil || calls != 2 {
		t.Errorf("force 应绕过缓存（calls=%d, err=%v）", calls, err)
	}

	// 不支持的 kind：返回 nil, nil。
	p2 := &config.Provider{Name: "np", BaseURL: "https://api.deepseek.com/v1"}
	if u, err = m.Query(context.Background(), p2, false); u != nil || err != nil {
		t.Errorf("未配置查询应返回 nil,nil；got %+v %v", u, err)
	}
}
