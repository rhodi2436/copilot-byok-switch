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
func quotaRow(name string, u *quota.Usage, errMsg string) [][]string {
	if errMsg != "" {
		return [][]string{{name, "查询失败", "", "", "", errMsg}}
	}
	if u == nil || len(u.Windows) == 0 {
		return [][]string{{name, "—", "", "", "", "无用量数据"}}
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
		rows = append(rows, []string{name, w.Label(), used, limit, pct, reset})
	}
	return rows
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
					Provider string       `json:"provider"`
					Kind     string       `json:"kind"`
					Usage    *quota.Usage `json:"usage"`
					Error    string       `json:"error"`
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
			for _, it := range out.Items {
				mark := "  "
				if it.Provider == out.Active {
					mark = "●"
				}
				for i, r := range quotaRow(it.Provider, it.Usage, it.Error) {
					if i == 0 {
						r = append([]string{mark}, r...)
					} else {
						r = append([]string{"  "}, r...)
					}
					rows = append(rows, r)
				}
			}
			printTable([]string{"", "供应商", "窗口", "已用", "额度", "占比", "重置于"}, rows)
			fmt.Println("\n● = 当前激活；占比取各窗口已用/额度，自动切换取占比最高的窗口判定。")
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
		for i := range cfg.Providers {
			p := &cfg.Providers[i]
			if name != "" && p.Name != name {
				continue
			}
			if quota.ResolveKind(p) == "" || quota.ResolveKind(p) == "none" {
				continue
			}
			found = true
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			u, qerr := m.Query(ctx, p, true)
			cancel()
			mark := "  "
			if p.Name == cfg.Active {
				mark = "●"
			}
			for i, r := range quotaRow(p.Name, u, errString(qerr)) {
				if i == 0 {
					r = append([]string{mark}, r...)
				} else {
					r = append([]string{" "}, r...)
				}
				rows = append(rows, r)
			}
		}
		if !found {
			fmt.Println("没有支持用量查询的供应商（守护进程未运行，直查上游）。")
			return nil
		}
		printTable([]string{"", "供应商", "窗口", "已用", "额度", "占比", "重置于"}, rows)
		fmt.Println("\n● = 当前激活；守护进程未运行 —— cops autoswitch 需要守护进程（cops serve / cops tray）。")
		return nil
	},
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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
	rootCmd.AddCommand(quotaCmd, autoswitchCmd)
}
