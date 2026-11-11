// autoswitch.go 限额自动切换引擎：守护进程内周期查询各供应商用量，
// 当前激活供应商任一窗口占比达到阈值时自动切换到候选顺序中第一个
// 未超阈值的供应商；全部超阈值时保持不变并记录事件。
package admin

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"cops/internal/config"
	"cops/internal/quota"
)

// autoSwitchEventMax 事件环形缓冲上限。
const autoSwitchEventMax = 50

// SwitchEvent 一次自动切换（或被阻塞的切换）记录。
type SwitchEvent struct {
	Time   time.Time `json:"time"`
	Kind   string    `json:"kind"` // "switch"（已切换）/ "blocked"（无可用候选）
	From   string    `json:"from,omitempty"`
	To     string    `json:"to,omitempty"`
	Reason string    `json:"reason"`
}

// ProviderCheck 单供应商的最近一次检查结果（Web/CLI 展示用）。
type ProviderCheck struct {
	Provider string        `json:"provider"`
	Kind     string        `json:"kind,omitempty"` // 用量查询类型；空 = 未配置
	Usage    *quota.Usage  `json:"usage,omitempty"`
	Error    string        `json:"error,omitempty"`
}

// CheckState 最近一轮检查的整体状态。
type CheckState struct {
	At           time.Time        `json:"at"`
	Active       string           `json:"active"`
	ThresholdPct float64          `json:"thresholdPct"`
	Checks       []ProviderCheck  `json:"checks"`
}

// autoSwitcher 引擎主体。所有方法并发安全（事件环与检查状态各持锁）。
type autoSwitcher struct {
	s    *Server
	poke chan struct{} // 配置变更后立即触发一轮检查（非阻塞）

	evMu   sync.Mutex
	events []SwitchEvent

	ckMu     sync.Mutex
	lastCheck *CheckState
}

func newAutoSwitcher(s *Server) *autoSwitcher {
	return &autoSwitcher{s: s, poke: make(chan struct{}, 1)}
}

// Start 启动后台循环（阻塞至 ctx 取消，需以 goroutine 调用）。
// 起步延迟 20s（等待守护进程与上游就绪），此后按配置间隔轮询；
// 配置变更（含启用/停用）最多一个间隔内生效，或经 poke 立即生效。
func (a *autoSwitcher) Start(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(20 * time.Second):
	}
	for {
		a.tick()
		interval := time.Duration(a.s.cfgPtr.Load().AutoSwitch.EffectiveIntervalSec()) * time.Second
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		case <-a.poke:
		}
	}
}

// Poke 请求尽快执行一轮检查（非阻塞）。
func (a *autoSwitcher) Poke() {
	select {
	case a.poke <- struct{}{}:
	default:
	}
}

// tick 执行一轮检查与决策。
func (a *autoSwitcher) tick() {
	cfg := a.s.cfgPtr.Load()
	if !cfg.AutoSwitch.Enabled {
		return
	}
	threshold := cfg.AutoSwitch.EffectiveThresholdPct()

	providers := make([]*config.Provider, 0, len(cfg.Providers))
	for i := range cfg.Providers {
		providers = append(providers, &cfg.Providers[i])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	snaps := a.s.quota.QueryMany(ctx, providers, true) // 决策绕过缓存

	a.recordCheck(&CheckState{
		At: time.Now(), Active: cfg.Active, ThresholdPct: threshold,
		Checks: checkList(cfg, snaps),
	})

	target, reason, ok := quota.Decide(cfg.Active, candidateNames(cfg), snaps, threshold)
	if target == "" && reason == "" {
		return // 未达阈值或当前供应商状态未知：无需动作
	}
	from := cfg.Active // Switch 会就地更新配置指针，先留痕
	if ok {
		if _, err := a.s.Switch(target, ""); err != nil {
			log.Printf("自动切换失败（%s → %s）: %v", from, target, err)
			a.recordEvent(SwitchEvent{Time: time.Now(), Kind: "blocked", From: from, To: target,
				Reason: reason + "；切换执行失败: " + err.Error()})
			return
		}
		log.Printf("限额自动切换：%s", reason)
		a.recordEvent(SwitchEvent{Time: time.Now(), Kind: "switch", From: from, To: target, Reason: reason})
		return
	}
	// 全部候选超阈值：保持不变，仅记一次（去重）避免刷屏。
	a.recordEvent(SwitchEvent{Time: time.Now(), Kind: "blocked", From: from, Reason: reason})
}

// candidateNames 候选供应商顺序：AutoSwitch.Order 优先，缺省为配置顺序。
func candidateNames(cfg *config.Config) []string {
	if len(cfg.AutoSwitch.Order) > 0 {
		return cfg.AutoSwitch.Order
	}
	names := make([]string, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		names = append(names, p.Name)
	}
	return names
}

// checkList 组装检查结果（按候选顺序，含查询类型与错误）。
func checkList(cfg *config.Config, snaps map[string]*quota.Snapshot) []ProviderCheck {
	out := make([]ProviderCheck, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		c := ProviderCheck{Provider: p.Name, Kind: quota.ResolveKind(&p)}
		if sn := snaps[p.Name]; sn != nil {
			c.Usage = sn.Usage
			if sn.Err != nil {
				c.Error = sn.Err.Error()
			}
		}
		out = append(out, c)
	}
	return out
}

// recordEvent 追加事件（环形上限）；与上一条完全同质（kind/from/to/reason
// 一致）的事件不重复记录，避免"全部候选超阈值"每轮刷屏。
func (a *autoSwitcher) recordEvent(ev SwitchEvent) {
	a.evMu.Lock()
	defer a.evMu.Unlock()
	if n := len(a.events); n > 0 {
		last := a.events[n-1]
		if last.Kind == ev.Kind && last.From == ev.From && last.To == ev.To && last.Reason == ev.Reason {
			return
		}
	}
	a.events = append(a.events, ev)
	if len(a.events) > autoSwitchEventMax {
		a.events = a.events[len(a.events)-autoSwitchEventMax:]
	}
}

// Events 返回事件副本（新的在前）。
func (a *autoSwitcher) Events() []SwitchEvent {
	a.evMu.Lock()
	defer a.evMu.Unlock()
	out := make([]SwitchEvent, len(a.events))
	copy(out, a.events)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// LastCheck 返回最近一轮检查状态副本（未检查过返回 nil）。
func (a *autoSwitcher) LastCheck() *CheckState {
	a.ckMu.Lock()
	defer a.ckMu.Unlock()
	if a.lastCheck == nil {
		return nil
	}
	cp := *a.lastCheck
	return &cp
}

func (a *autoSwitcher) recordCheck(st *CheckState) {
	a.ckMu.Lock()
	a.lastCheck = st
	a.ckMu.Unlock()
}

// handleQuota GET /_cops/api/quota[?name=glm&refresh=1]：查询供应商用量
// （多窗口）。不指定 name 时返回全部支持查询的供应商。
func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	cfg := s.cfgPtr.Load()
	refresh := r.URL.Query().Get("refresh") == "1"
	name := r.URL.Query().Get("name")

	type item struct {
		Provider string       `json:"provider"`
		Kind     string       `json:"kind"`
		Usage    *quota.Usage `json:"usage,omitempty"`
		Error    string       `json:"error,omitempty"`
	}
	items := []item{}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		kind := quota.ResolveKind(p)
		if kind == "" || kind == "none" {
			continue
		}
		if name != "" && p.Name != name {
			continue
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		u, err := s.quota.Query(ctx, p, refresh)
		cancel()
		it := item{Provider: p.Name, Kind: kind}
		if u != nil {
			it.Usage = u
		}
		if err != nil {
			it.Error = err.Error()
		}
		items = append(items, it)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "active": cfg.Active})
}

// handleAutoSwitch GET/PUT /_cops/api/autoswitch：读写自动切换配置；
// GET 附带最近事件与最近一轮检查状态。
func (s *Server) handleAutoSwitch(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		resp := map[string]any{
			"autoSwitch": s.cfgPtr.Load().AutoSwitch,
			"events":     s.switcher.Events(),
		}
		if lc := s.switcher.LastCheck(); lc != nil {
			resp["lastCheck"] = lc
		}
		writeJSON(w, http.StatusOK, resp)
	case http.MethodPut, http.MethodPost:
		old := s.cfgPtr.Load()
		var req struct {
			AutoSwitch config.AutoSwitchConfig `json:"autoSwitch"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		newCfg := *old
		newCfg.AutoSwitch = req.AutoSwitch
		// 先按原始输入校验（悬空候选显式报错），再清洗（trim/去重）。
		if err := newCfg.ValidateAutoSwitch(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		newCfg.AutoSwitch.Normalize(newCfg.Providers)
		if err := newCfg.Save(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		s.cfgPtr.Store(&newCfg)
		s.switcher.Poke() // 启用/阈值变更后立即检查一轮
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "autoSwitch": newCfg.AutoSwitch})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
