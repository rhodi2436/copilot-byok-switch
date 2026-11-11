package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"cops/internal/config"
	"cops/internal/quota"
)

// quotaRow 渲染一个供应商的多窗口用量。
func quotaRow(name, scope string, u *quota.Usage, errMsg string) [][]string {
	if errMsg != "" {
		return [][]string{{name, scope, "查询失败", "", "", "", errMsg}}
	}
	if u == nil || len(u.Windows) == 0 {
		return [][]string{{name, scope, "—", "", "", "", "无用量数据"}}
	}
	var rows [][]string
	for _, w := range u.Windows {
		reset := "—"
		if !w.ResetAt.IsZero() {
			reset = w.ResetAt.Format("01-02 15:04")
		}
		used, limit := fmt.Sprintf("%.0f", w.Used), "未知"
		pct := "—"
		if w.Valid() {
			limit = fmt.Sprintf("%.0f", w.Limit)
			pct = fmt.Sprintf("%.1f%%", w.Percent())
		}
		rows = append(rows, []string{name, scope, w.Label(), used, limit, pct, reset})
	}
	return rows
}

// scopeZh 查询范围中文。
func scopeZh(scope string) string {
	if scope == "team" {
		return "团队"
	}
	return "个人"
}

// warnDuplicates 打印相同用量警告（两个供应商查出完全一致的用量 =
// 疑似同一套餐或团队查询落回个人范围）。
func warnDuplicates(dups []string) {
	if len(dups) == 0 {
		return
	}
	fmt.Printf("\n⚠  %s 查询到与其他供应商完全相同的用量 —— 若它们分属不同套餐（如团队项目与个人），\n"+
		"   团队供应商需绑定查询范围：cops quota team <名称> --org <组织ID> --project <项目ID>（ID 获取方式见 README）。\n",
		strings.Join(dups, "、"))
}

var quotaCmd = &cobra.Command{
	Use:   "quota [名称]",
	Short: "查询供应商限额用量（多窗口：5小时/周/月等，订阅套餐如 GLM Coding Plan）",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := ""
		if len(args) > 0 {
			name = args[0]
		}

		// 守护进程在线：走管理 API（共享用量缓存）。
		if base, ok := daemonAPI(); ok {
			url := base + "/_cops/api/quota?refresh=1"
			if name != "" {
				url += "&name=" + name
			}
			resp, err := (&http.Client{Timeout: 30 * time.Second}).Get(url)
			if err != nil {
				return fmt.Errorf("调用守护进程失败: %w", err)
			}
			defer resp.Body.Close()
			var out struct {
				Items []struct {
					Provider    string       `json:"provider"`
					Kind        string       `json:"kind"`
					Scope       string       `json:"scope"`
					Usage       *quota.Usage `json:"usage"`
					Error       string       `json:"error"`
					DuplicateOf string       `json:"duplicateOf"`
				} `json:"items"`
				Active string `json:"active"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				return fmt.Errorf("解析响应失败: %w", err)
			}
			if len(out.Items) == 0 {
				fmt.Println("没有支持用量查询的供应商（订阅套餐如 GLM Coding Plan 需在配置中启用，详见 README）。")
				return nil
			}
			rows := [][]string{}
			var dups []string
			for _, it := range out.Items {
				mark := "  "
				if it.Provider == out.Active {
					mark = "●"
				}
				if it.DuplicateOf != "" {
					dups = append(dups, it.Provider+"（与 "+it.DuplicateOf+" 相同）")
				}
				for i, r := range quotaRow(it.Provider, scopeZh(it.Scope), it.Usage, it.Error) {
					if i == 0 {
						r = append([]string{mark}, r...)
					} else {
						r = append([]string{"  "}, r...)
					}
					rows = append(rows, r)
				}
			}
			printTable([]string{"", "供应商", "范围", "窗口", "已用", "额度", "占比", "重置于"}, rows)
			fmt.Println("\n● = 当前激活；占比取各窗口已用/额度，自动切换取占比最高的窗口判定。")
			warnDuplicates(dups)
			return nil
		}

		// 离线：直查上游。
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		m := quota.NewManager(nil, 0)
		rows := [][]string{}
		found := false
		type fetched struct {
			name string
			u    *quota.Usage
		}
		var results []fetched
		for i := range cfg.Providers {
			p := &cfg.Providers[i]
			if name != "" && p.Name != name {
				continue
			}
			if quota.ResolveKind(p) == "" || quota.ResolveKind(p) == "none" {
				continue
			}
			found = true
			scope := "个人"
			if p.Quota.IsTeamScope() {
				scope = "团队"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			u, qerr := m.Query(ctx, p, true)
			cancel()
			mark := "  "
			if p.Name == cfg.Active {
				mark = "●"
			}
			for i, r := range quotaRow(p.Name, scope, u, errString(qerr)) {
				if i == 0 {
					r = append([]string{mark}, r...)
				} else {
					r = append([]string{" "}, r...)
				}
				rows = append(rows, r)
			}
			if u != nil {
				results = append(results, fetched{p.Name, u})
			}
		}
		if !found {
			fmt.Println("没有支持用量查询的供应商（守护进程未运行，直查上游）。")
			return nil
		}
		printTable([]string{"", "供应商", "范围", "窗口", "已用", "额度", "占比", "重置于"}, rows)
		fmt.Println("\n● = 当前激活；守护进程未运行 —— cops autoswitch 需要守护进程（cops serve / cops tray）。")
		var dups []string
		for i, a := range results {
			for _, b := range results[:i] {
				if a.u.SameUsageAs(b.u) {
					dups = append(dups, a.name+"（与 "+b.name+" 相同）")
					break
				}
			}
		}
		warnDuplicates(dups)
		return nil
	},
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ── cops quota team / unbind：绑定/解除团队查询范围 ───────────────

// setQuotaScope 修改供应商用量查询范围：org/project 均非空 = 团队范围，
// 均空 = 回到个人范围。在线走 providers API（守护进程内存同步），离线改配置。
func setQuotaScope(name, org, project string) error {
	apply := func(p *config.Provider) {
		if p.Quota == nil {
			p.Quota = &config.QuotaConfig{}
		}
		p.Quota.OrganizationID = org
		p.Quota.ProjectID = project
		if *p.Quota == (config.QuotaConfig{}) {
			p.Quota = nil // 清空后无残留
		}
	}

	if base, ok := daemonAPI(); ok {
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(base + "/_cops/api/providers")
		if err != nil {
			return fmt.Errorf("调用守护进程失败: %w", err)
		}
		var list struct {
			Providers []config.Provider `json:"providers"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
			resp.Body.Close()
			return fmt.Errorf("解析响应失败: %w", err)
		}
		resp.Body.Close()
		var target *config.Provider
		for i := range list.Providers {
			if list.Providers[i].Name == name {
				target = &list.Providers[i]
			}
		}
		if target == nil {
			return fmt.Errorf("供应商不存在: %s", name)
		}
		apply(target)
		// GET 返回的是脱敏 Key；回传空值让服务端保留原值
		// （脱敏形如 "4065b1...OYIv"，不匹配 "***" 前缀，原样回传会覆盖真实 Key）。
		target.APIKey = ""
		if target.Quota != nil {
			target.Quota.APIKey = ""
		}
		body, _ := json.Marshal(target)
		presp, err := (&http.Client{Timeout: 5 * time.Second}).Post(base+"/_cops/api/providers", "application/json", bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("调用守护进程失败: %w", err)
		}
		defer presp.Body.Close()
		var out struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(presp.Body).Decode(&out)
		if presp.StatusCode != http.StatusOK {
			return fmt.Errorf("保存失败: %s", out.Error)
		}
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	p, _ := cfg.Find(name)
	if p == nil {
		return fmt.Errorf("供应商不存在: %s", name)
	}
	apply(p)
	if err := p.Validate(); err != nil {
		return err
	}
	return cfg.Save()
}

var quotaTeamCmd = &cobra.Command{
	Use:   "team <名称> --org <组织ID> --project <项目ID>",
	Short: "绑定团队（组织/项目）用量查询范围（GLM 团队版 Coding Plan 必需）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, _ := cmd.Flags().GetString("org")
		project, _ := cmd.Flags().GetString("project")
		if strings.TrimSpace(org) == "" || strings.TrimSpace(project) == "" {
			return fmt.Errorf("--org 与 --project 必须同时提供（ID 获取方式见 README「团队版」一节）")
		}
		if err := setQuotaScope(args[0], strings.TrimSpace(org), strings.TrimSpace(project)); err != nil {
			return err
		}
		fmt.Printf("✅ %s 已绑定团队查询范围（organization=%s, project=%s），运行 cops quota 验证。\n",
			args[0], org, project)
		return nil
	},
}

var quotaUnbindCmd = &cobra.Command{
	Use:   "unbind <名称>",
	Short: "解除团队查询范围，回到个人范围查询",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := setQuotaScope(args[0], "", ""); err != nil {
			return err
		}
		fmt.Printf("✅ %s 已回到个人范围查询，运行 cops quota 验证。\n", args[0])
		return nil
	},
}

// ── cops autoswitch ──────────────────────────────────────────────

// printAutoSwitchState 打印自动切换配置与最近事件（daemon 在线时含事件与最近检查）。
func printAutoSwitchState(a config.AutoSwitchConfig, events []map[string]any, lastCheck map[string]any) {
	state := "停用 ✗"
	if a.Enabled {
		state = "启用 ✓"
	}
	fmt.Printf("状态: %s\n阈值: %.0f%%（任一窗口占比达到即切换）\n轮询间隔: %ds\n",
		state, a.EffectiveThresholdPct(), a.EffectiveIntervalSec())
	if len(a.Order) > 0 {
		fmt.Printf("候选顺序: %s\n", strings.Join(a.Order, " → "))
	} else {
		fmt.Println("候选顺序: 未设置（按配置文件中的供应商顺序）")
	}
	if lastCheck != nil {
		if at, ok := lastCheck["at"].(string); ok {
			fmt.Printf("最近检查: %s\n", at)
		}
	}
	if len(events) > 0 {
		fmt.Println("最近事件:")
		for _, ev := range events {
			t, _ := ev["time"].(string)
			kind, _ := ev["kind"].(string)
			from, _ := ev["from"].(string)
			to, _ := ev["to"].(string)
			reason, _ := ev["reason"].(string)
			arrow := from + " → " + to
			if kind == "blocked" {
				arrow = from + "（保持）"
			}
			fmt.Printf("  %s  [%s] %s  %s\n", fmtEventTime(t), map[bool]string{true: "已切换", false: "未切换"}[kind == "switch"], arrow, reason)
		}
	} else {
		fmt.Println("最近事件: 暂无")
	}
}

// fmtEventTime 把 RFC3339 时间裁剪为易读的 "MM-dd HH:mm:ss"。
func fmtEventTime(t string) string {
	if len(t) >= 19 {
		return strings.ReplaceAll(t[5:19], "T", " ")
	}
	return t
}

var autoswitchCmd = &cobra.Command{
	Use:   "autoswitch",
	Short: "查看限额自动切换配置与最近事件",
	RunE: func(cmd *cobra.Command, args []string) error {
		if base, ok := daemonAPI(); ok {
			resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(base + "/_cops/api/autoswitch")
			if err != nil {
				return fmt.Errorf("调用守护进程失败: %w", err)
			}
			defer resp.Body.Close()
			var out struct {
				AutoSwitch config.AutoSwitchConfig `json:"autoSwitch"`
				Events     []map[string]any        `json:"events"`
				LastCheck  map[string]any          `json:"lastCheck"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				return fmt.Errorf("解析响应失败: %w", err)
			}
			printAutoSwitchState(out.AutoSwitch, out.Events, out.LastCheck)
			return nil
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		printAutoSwitchState(cfg.AutoSwitch, nil, nil)
		fmt.Println("\n（守护进程未运行，事件与检查状态仅运行时可见；cops autoswitch on/off/set 仍可离线配置）")
		return nil
	},
}

// putAutoSwitchDaemon 经守护进程写入自动切换配置。
func putAutoSwitchDaemon(base string, a config.AutoSwitchConfig) error {
	body, _ := json.Marshal(map[string]any{"autoSwitch": a})
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Post(base+"/_cops/api/autoswitch", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("调用守护进程失败: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK || !out.OK {
		return fmt.Errorf("保存失败: %s", out.Error)
	}
	return nil
}

// saveAutoSwitch 读取-合并-校验-保存自动切换配置；在线时走守护进程。
func saveAutoSwitch(merge func(*config.AutoSwitchConfig)) error {
	if base, ok := daemonAPI(); ok {
		var cur config.AutoSwitchConfig
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(base + "/_cops/api/autoswitch")
		if err != nil {
			return fmt.Errorf("调用守护进程失败: %w", err)
		}
		if err := json.NewDecoder(resp.Body).Decode(&struct {
			AutoSwitch *config.AutoSwitchConfig `json:"autoSwitch"`
		}{AutoSwitch: &cur}); err != nil {
			return fmt.Errorf("解析响应失败: %w", err)
		}
		resp.Body.Close()
		merge(&cur)
		return putAutoSwitchDaemon(base, cur)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	merge(&cfg.AutoSwitch)
	// 先校验（悬空候选显式报错）再清洗，与管理 API 行为一致。
	if err := cfg.ValidateAutoSwitch(); err != nil {
		return err
	}
	cfg.AutoSwitch.Normalize(cfg.Providers)
	if err := cfg.Save(); err != nil {
		return err
	}
	return nil
}

var autoswitchOnCmd = &cobra.Command{
	Use:   "on",
	Short: "启用限额自动切换",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := saveAutoSwitch(func(a *config.AutoSwitchConfig) { a.Enabled = true }); err != nil {
			return err
		}
		fmt.Println("✅ 限额自动切换已启用（守护进程运行时生效）。")
		return nil
	},
}

var autoswitchOffCmd = &cobra.Command{
	Use:   "off",
	Short: "停用限额自动切换",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := saveAutoSwitch(func(a *config.AutoSwitchConfig) { a.Enabled = false }); err != nil {
			return err
		}
		fmt.Println("✅ 限额自动切换已停用。")
		return nil
	},
}

var autoswitchSetCmd = &cobra.Command{
	Use:   "set",
	Short: "修改自动切换参数（--threshold/--interval/--order 任意组合）",
	RunE: func(cmd *cobra.Command, args []string) error {
		th, _ := cmd.Flags().GetFloat64("threshold")
		iv, _ := cmd.Flags().GetInt("interval")
		order, _ := cmd.Flags().GetStringSlice("order")
		if err := saveAutoSwitch(func(a *config.AutoSwitchConfig) {
			if cmd.Flags().Changed("threshold") {
				a.ThresholdPct = th
			}
			if cmd.Flags().Changed("interval") {
				a.IntervalSec = iv
			}
			if cmd.Flags().Changed("order") {
				a.Order = order
			}
		}); err != nil {
			return err
		}
		fmt.Println("✅ 已保存自动切换参数。")
		return nil
	},
}

func init() {
	autoswitchSetCmd.Flags().Float64("threshold", 95, "触发阈值百分比（如 95 = 用量达到 95% 切换）")
	autoswitchSetCmd.Flags().Int("interval", 300, "用量轮询间隔（秒，最小 30）")
	autoswitchSetCmd.Flags().StringSlice("order", nil, "候选供应商优先顺序（逗号分隔，如 glm2,glm,deepseek）")
	autoswitchCmd.AddCommand(autoswitchOnCmd, autoswitchOffCmd, autoswitchSetCmd)
	quotaTeamCmd.Flags().String("org", "", "团队组织 ID（bigmodel-organization）")
	quotaTeamCmd.Flags().String("project", "", "团队项目 ID（bigmodel-project）")
	quotaCmd.AddCommand(quotaTeamCmd, quotaUnbindCmd)
	rootCmd.AddCommand(quotaCmd, autoswitchCmd)
}
