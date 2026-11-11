// Package quota 查询订阅制供应商的限额用量，并按阈值决策自动切换。
// 订阅套餐（如 GLM Coding Plan）通常有多重限额窗口（5 小时 / 周 / 月），
// 查询结果统一为窗口列表，切换判定取占比最高（约束最紧）的窗口。
package quota

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"cops/internal/config"
)

// Window 一个限额窗口的用量。单位随供应商而定（credits / points / tokens），
// 判定与展示只用比例，不依赖单位含义。
type Window struct {
	// Type 窗口标识：five_hour / weekly / monthly 等（展示层映射中文）。
	Type string  `json:"type"`
	Used float64 `json:"used"`
	// Limit 窗口总额度；0 = 上游未返回额度（无限制或未知）。
	Limit float64 `json:"limit"`
	// ResetAt 窗口重置时间；未知为零值。
	ResetAt time.Time `json:"resetAt,omitempty"`
}

// Percent 已用比例（0-100）。Limit<=0 时返回 0（无法计算）。
func (w Window) Percent() float64 {
	if w.Limit <= 0 {
		return 0
	}
	return w.Used / w.Limit * 100
}

// Valid 该窗口是否有可比限额。
func (w Window) Valid() bool { return w.Limit > 0 }

// Label 窗口的中文标签。
func (w Window) Label() string {
	switch w.Type {
	case "five_hour", "5h":
		return "5小时"
	case "weekly", "week":
		return "周"
	case "monthly", "month":
		return "月"
	case "daily", "day":
		return "日"
	}
	if w.Type == "" {
		return "额度"
	}
	return w.Type
}

// Usage 单个供应商的用量查询结果。
type Usage struct {
	Provider  string    `json:"provider"`
	Windows   []Window  `json:"windows"`
	FetchedAt time.Time `json:"fetchedAt"`
}

// Binding 返回占比最高（约束最紧）的窗口；无有效限额窗口返回零值 false。
func (u *Usage) Binding() (Window, bool) {
	var best Window
	found := false
	for _, w := range u.Windows {
		if !w.Valid() {
			continue
		}
		if !found || w.Percent() > best.Percent() {
			best, found = w, true
		}
	}
	return best, found
}

// BindingPercent 约束最紧窗口的已用比例（0-100）；无可比窗口返回 0。
func (u *Usage) BindingPercent() float64 {
	w, ok := u.Binding()
	if !ok {
		return 0
	}
	return w.Percent()
}

// FormatWindows 人类可读的多窗口摘要（如 "5小时 96.2% · 月 41.0%"），供 CLI/托盘。
func (u *Usage) FormatWindows() string {
	if u == nil || len(u.Windows) == 0 {
		return "无用量数据"
	}
	parts := make([]string, 0, len(u.Windows))
	for _, w := range u.Windows {
		if w.Valid() {
			parts = append(parts, fmt.Sprintf("%s %.1f%%", w.Label(), w.Percent()))
		} else {
			parts = append(parts, fmt.Sprintf("%s 已用 %.0f（额度未知）", w.Label(), w.Used))
		}
	}
	return strings.Join(parts, " · ")
}

// Manager 用量查询入口：按供应商 Quota 配置分发到具体实现，带短 TTL 缓存
// （CLI 查询与自动切换引擎共用，避免连续请求打爆上游）。
type Manager struct {
	client *http.Client
	// cacheTTL 查询结果缓存时长；0 = 不缓存（测试用）。
	cacheTTL time.Duration

	mu    sync.Mutex
	cache map[string]cachedUsage
}

type cachedUsage struct {
	usage *Usage
	err   error
	at    time.Time
}

// NewManager 创建查询管理器。
func NewManager(client *http.Client, cacheTTL time.Duration) *Manager {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &Manager{client: client, cacheTTL: cacheTTL, cache: map[string]cachedUsage{}}
}

// ResolveKind 返回供应商生效的用量查询类型：
// 显式配置（"none" → 不查询）优先，否则按 BaseURL 域名探测；空 = 不支持。
func ResolveKind(p *config.Provider) string {
	if p.Quota != nil && p.Quota.Kind != "" {
		return p.Quota.Kind
	}
	return DetectKind(p.BaseURL)
}

// DetectKind 按 BaseURL 域名探测用量查询类型；无法识别返回空串。
func DetectKind(baseURL string) string {
	u := strings.ToLower(baseURL)
	switch {
	case strings.Contains(u, "bigmodel.cn") || strings.Contains(u, "z.ai"):
		return "glm"
	}
	return ""
}

// querySource 单类供应商的用量查询实现。
type querySource func(ctx context.Context, hc *http.Client, p *config.Provider) (*Usage, error)

// sources 已注册的查询实现（内置 kind 惰性注册，测试可注入假实现）。
var sources = map[string]querySource{
	"glm": queryGLM,
}

// Query 查询单个供应商用量。未配置/不支持返回 nil（不是错误）；
// 查询失败返回错误。结果按 cacheTTL 缓存；force=true 绕过缓存。
func (m *Manager) Query(ctx context.Context, p *config.Provider, force bool) (*Usage, error) {
	kind := ResolveKind(p)
	if kind == "" || kind == "none" {
		return nil, nil
	}
	src, ok := sources[kind]
	if !ok {
		return nil, fmt.Errorf("供应商 %s 的用量查询类型 %q 未实现", p.Name, kind)
	}
	if m.cacheTTL > 0 && !force {
		if c, ok := m.loadCache(p.Name); ok {
			return c.usage, c.err
		}
	}
	u, err := src(ctx, m.client, p)
	if u != nil {
		u.Provider = p.Name
		u.FetchedAt = time.Now()
	}
	if m.cacheTTL > 0 {
		m.storeCache(p.Name, u, err)
	}
	return u, err
}

// QueryMany 并发查询一组供应商，返回每个供应商的快照（含不支持查询的，
// 其 Usage/Err 均为 nil = 用量未知，在决策中等价于"未见限额"）。
// force=true 绕过缓存（自动切换决策用，保证基于新鲜数据）。
func (m *Manager) QueryMany(ctx context.Context, ps []*config.Provider, force bool) map[string]*Snapshot {
	var mu sync.Mutex
	snaps := make(map[string]*Snapshot, len(ps))
	var wg sync.WaitGroup
	for _, p := range ps {
		wg.Add(1)
		go func(p *config.Provider) {
			defer wg.Done()
			u, err := m.Query(ctx, p, force)
			mu.Lock()
			snaps[p.Name] = &Snapshot{Usage: u, Err: err}
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	return snaps
}

func (m *Manager) loadCache(name string) (cachedUsage, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.cache[name]
	if !ok || time.Since(c.at) > m.cacheTTL {
		return cachedUsage{}, false
	}
	return c, true
}

func (m *Manager) storeCache(name string, u *Usage, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cache[name] = cachedUsage{usage: u, err: err, at: time.Now()}
}

// Snapshot 一个供应商的用量查询结果快照（决策输入）。
type Snapshot struct {
	// Usage 用量；nil = 未配置查询或查询失败（见 Err）。
	Usage *Usage
	Err   error
}

// Percent 快照的约束最紧窗口占比；无用量返回 0。
func (s *Snapshot) Percent() float64 {
	if s == nil || s.Usage == nil {
		return 0
	}
	return s.Usage.BindingPercent()
}

// Decide 自动切换决策：active 任一窗口占比达到阈值且存在可用候选时，
// 返回 (目标供应商, 原因, true)。
//
//	候选可用 = 用量未知（未配置查询 / 不支持）或约束窗口占比 < 阈值；
//	查询失败的候选视为不可用（状态不明不切）。
//	候选按 candidates 顺序取第一个可用者（Order 配置优先，缺省为配置顺序）。
func Decide(active string, candidates []string, snaps map[string]*Snapshot, thresholdPct float64) (string, string, bool) {
	sn := snaps[active]
	if sn == nil || sn.Err != nil || sn.Usage == nil {
		return "", "", false // 当前供应商状态未知：不动作，避免误切
	}
	w, ok := sn.Usage.Binding()
	if !ok || w.Percent() < thresholdPct {
		return "", "", false // 未达阈值
	}
	for _, name := range candidates {
		if name == active {
			continue
		}
		c := snaps[name]
		if c != nil && c.Err != nil {
			continue // 查询失败的候选不切
		}
		if c != nil && c.Usage != nil {
			if cw, has := c.Usage.Binding(); has && cw.Percent() >= thresholdPct {
				continue // 候选也超阈值
			}
		}
		candDesc := "用量未知"
		if c != nil && c.Usage != nil {
			candDesc = c.Usage.FormatWindows()
		}
		reason := fmt.Sprintf("%s %s窗口用量 %.1f%% ≥ %.0f%% 阈值 → 切换到 %s（%s）",
			active, w.Label(), w.Percent(), thresholdPct, name, candDesc)
		return name, reason, true
	}
	return "", fmt.Sprintf("%s %s窗口用量 %.1f%% ≥ %.0f%% 阈值，但全部候选均超阈值或不可用，保持不变",
		active, w.Label(), w.Percent(), thresholdPct), false
}
