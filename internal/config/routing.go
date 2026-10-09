// routing.go 代理侧模型路由配置：虚拟模型表 + utility 辅助模型识别。
// 背景：个人 BYOK 下 Copilot CLI 的 /model 不可用，Agent 无法自选模型，
// 因此在代理层按请求体 model 字段做确定性路由（见 ROADMAP v0.2）。
package config

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// RouteTarget 路由目标：供应商名 + 真实模型名。
type RouteTarget struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// ContextWindow 该虚拟模型的上下文窗口覆盖值（token 数）；
	// 0 = 未设置，回退目标供应商真实模型的 ModelContext 值。
	ContextWindow int `json:"contextWindow,omitempty"`
}

// RoutingConfig 模型路由配置。零值（旧配置无 routing 字段）时全部请求走 default 兜底，
// 行为与 v0.1 完全一致。
type RoutingConfig struct {
	// VirtualModels 虚拟模型名 → 目标。请求 model 命中时改写到目标供应商/模型，
	// 可跨供应商；CLI 的 COPILOT_MODEL 只需指向钉住名。
	VirtualModels map[string]RouteTarget `json:"virtualModels,omitempty"`
	// DefaultVirtual 默认虚拟模型名（"model use" 写此字段）。
	// 钉住名请求（cfg.VirtualModel）且该名在表内时路由到它。
	DefaultVirtual string `json:"defaultVirtual,omitempty"`
	// Utility 辅助调用目标：Copilot CLI 会把 gpt-5.4-nano 等内部 id 直发
	// BYOK 端点用于 compaction/auto-mode（copilot-cli#4950），命中模式时路由到此。
	Utility *RouteTarget `json:"utility,omitempty"`
	// UtilityPatterns glob 模式列表（path.Match 语法）；为空时用 DefaultUtilityPatterns。
	UtilityPatterns []string `json:"utilityPatterns,omitempty"`
}

// DefaultUtilityPatterns 内置辅助模型匹配模式。
var DefaultUtilityPatterns = []string{"*nano*", "*-mini*", "*fast*"}

// EffectiveUtilityPatterns 返回生效的模式列表。
func (r *RoutingConfig) EffectiveUtilityPatterns() []string {
	if len(r.UtilityPatterns) > 0 {
		return r.UtilityPatterns
	}
	return DefaultUtilityPatterns
}

// MatchesUtility 判断 model 是否命中辅助模型模式（仅当配置了 Utility 目标）。
func (r *RoutingConfig) MatchesUtility(model string) bool {
	if r.Utility == nil || model == "" {
		return false
	}
	for _, pat := range r.EffectiveUtilityPatterns() {
		if ok, err := path.Match(pat, model); err == nil && ok {
			return true
		}
	}
	return false
}

// Normalize 去除两端空白，保证存储整洁（幂等）。
func (r *RoutingConfig) Normalize() {
	r.DefaultVirtual = strings.TrimSpace(r.DefaultVirtual)
	for k, t := range r.VirtualModels {
		t.Provider = strings.TrimSpace(t.Provider)
		t.Model = strings.TrimSpace(t.Model)
		nk := strings.TrimSpace(k)
		if nk == k {
			r.VirtualModels[k] = t
			continue
		}
		delete(r.VirtualModels, k)
		if nk != "" {
			r.VirtualModels[nk] = t
		}
	}
	if r.Utility != nil {
		r.Utility.Provider = strings.TrimSpace(r.Utility.Provider)
		r.Utility.Model = strings.TrimSpace(r.Utility.Model)
		if *r.Utility == (RouteTarget{}) {
			r.Utility = nil // 空目标视为未配置
		}
	}
	for i, p := range r.UtilityPatterns {
		r.UtilityPatterns[i] = strings.TrimSpace(p)
	}
	r.UtilityPatterns = pruneEmpty(r.UtilityPatterns)
}

func pruneEmpty(ss []string) []string {
	out := ss[:0]
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// SortedVirtualModels 返回按名排序的虚拟模型键，用于稳定展示。
func (r *RoutingConfig) SortedVirtualModels() []string {
	names := make([]string, 0, len(r.VirtualModels))
	for k := range r.VirtualModels {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// TargetContext 返回路由目标的生效上下文窗口（token 数）：
// 虚拟覆盖值 > 0 优先，否则回退目标供应商 ModelContext 中真实模型的值；未知返回 0。
func (c *Config) TargetContext(t RouteTarget) int {
	if t.ContextWindow > 0 {
		return t.ContextWindow
	}
	if p, _ := c.Find(t.Provider); p != nil {
		return p.ModelContext[t.Model]
	}
	return 0
}

// TargetMaxOutput 返回路由目标真实模型的最大输出 token 数
// （回退目标供应商 ModelMaxOutput；未配置返回 0 = 不限制）。
func (c *Config) TargetMaxOutput(t RouteTarget) int {
	if p, _ := c.Find(t.Provider); p != nil {
		return p.ModelMaxOutput[t.Model]
	}
	return 0
}

// validateTarget 校验单个路由目标。
func (c *Config) validateTarget(where string, t RouteTarget) error {
	if strings.TrimSpace(t.Provider) == "" {
		return fmt.Errorf("路由目标 %s: 未指定供应商", where)
	}
	if _, i := c.Find(strings.TrimSpace(t.Provider)); i < 0 {
		return fmt.Errorf("路由目标 %s: 供应商 %q 不存在", where, t.Provider)
	}
	if strings.TrimSpace(t.Model) == "" {
		return fmt.Errorf("路由目标 %s: 未指定模型", where)
	}
	if t.ContextWindow < 0 {
		return fmt.Errorf("路由目标 %s: 上下文窗口不能为负数: %d", where, t.ContextWindow)
	}
	return nil
}

// ValidateRouting 校验路由配置引用完整性。
func (c *Config) ValidateRouting() error {
	r := &c.Routing
	for name, t := range r.VirtualModels {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("虚拟模型表存在空名称")
		}
		if err := c.validateTarget("「"+name+"」", t); err != nil {
			return err
		}
	}
	if r.DefaultVirtual != "" {
		if _, ok := r.VirtualModels[r.DefaultVirtual]; !ok {
			return fmt.Errorf("默认虚拟模型 %q 不在虚拟模型表中", r.DefaultVirtual)
		}
	}
	if r.Utility != nil {
		if err := c.validateTarget("utility", *r.Utility); err != nil {
			return err
		}
	}
	for _, pat := range r.UtilityPatterns {
		if _, err := path.Match(pat, ""); err != nil {
			return fmt.Errorf("utility 模式 %q 不合法: %v", pat, err)
		}
	}
	return nil
}
