// model.go cops model 子命令：代理侧虚拟模型路由管理（v0.2）。
// 背景：个人 BYOK 下 Copilot CLI 的 /model 不可用，模型选择在代理层完成，
// 客户端只需固定使用钉住名（COPILOT_MODEL），由 cops 按表路由到真实模型。
package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"cops/internal/config"
)

// fetchRouting 读取守护进程内存中的路由配置（daemon 是配置真源）。
func fetchRouting(base string) (config.RoutingConfig, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(base + "/_cops/api/routing")
	if err != nil {
		return config.RoutingConfig{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return config.RoutingConfig{}, fmt.Errorf("守护进程不支持模型路由（HTTP %d）：请用新版 cops 重启托盘/守护进程", resp.StatusCode)
	}
	var out struct {
		Routing config.RoutingConfig `json:"routing"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return config.RoutingConfig{}, err
	}
	return out.Routing, nil
}

// pushRouting 全量写回路由配置（服务端校验）。
func pushRouting(base string, r config.RoutingConfig) error {
	body, _ := json.Marshal(map[string]any{"routing": r})
	req, _ := http.NewRequest(http.MethodPut, base+"/_cops/api/routing", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK || !out.OK {
		if out.Error != "" {
			return fmt.Errorf("%s", out.Error)
		}
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// loadRoutingForWrite 写路径统一入口：daemon 在线取内存配置，离线读本地文件。
func loadRoutingForWrite() (r config.RoutingConfig, base string, online bool, err error) {
	if base, ok := daemonAPI(); ok {
		r, err = fetchRouting(base)
		if err != nil {
			return config.RoutingConfig{}, "", false, fmt.Errorf("读取守护进程路由失败: %w", err)
		}
		return r, base, true, nil
	}
	cfg, err := config.Load()
	if err != nil {
		return config.RoutingConfig{}, "", false, err
	}
	return cfg.Routing, "", false, nil
}

// saveRouting 写路径统一出口。
func saveRouting(base string, online bool, r config.RoutingConfig) error {
	if online {
		return pushRouting(base, r)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Routing = r
	cfg.Routing.Normalize()
	if err := cfg.ValidateRouting(); err != nil {
		return err
	}
	return cfg.Save()
}

// fetchProviders 读取守护进程的供应商列表（用于回退查 ModelContext）。
func fetchProviders(base string) ([]config.Provider, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(base + "/_cops/api/providers")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var out struct {
		Providers []config.Provider `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Providers, nil
}

// fmtContextTokens 把 token 数格式化为 128k / 1.5m 风格简写。
func fmtContextTokens(n int) string {
	trim := func(s string) string { return strings.TrimSuffix(strings.TrimSuffix(s, "0"), ".") }
	switch {
	case n >= 1_000_000:
		return trim(fmt.Sprintf("%.1f", float64(n)/1e6)) + "m"
	case n >= 1_000:
		return trim(fmt.Sprintf("%.1f", float64(n)/1e3)) + "k"
	default:
		return fmt.Sprintf("%d", n)
	}
}

var modelCmd = &cobra.Command{
	Use:   "model",
	Short: "虚拟模型路由管理（代理侧选模型，替代不可用的 /model）",
	Long: `管理代理侧的虚拟模型路由表。

Copilot CLI 固定使用钉住名（默认 cops-active）发请求，cops 按路由表
把它改写到真实供应商/模型；切换默认模型用 cops model use，即时生效。`,
}

var modelListCmd = &cobra.Command{
	Use:   "list",
	Short: "列出虚拟模型路由",
	RunE: func(cmd *cobra.Command, args []string) error {
		var r config.RoutingConfig
		// ctxSrc 仅用于计算回退上下文（真实模型级 ModelContext）。
		var ctxSrc *config.Config
		if base, ok := daemonAPI(); ok {
			got, err := fetchRouting(base)
			if err != nil {
				return fmt.Errorf("读取守护进程路由失败: %w", err)
			}
			r = got
			if plist, err := fetchProviders(base); err == nil {
				ctxSrc = &config.Config{Providers: plist}
			}
		} else {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			r = cfg.Routing
			ctxSrc = cfg
		}

		if len(r.VirtualModels) == 0 && r.Utility == nil {
			fmt.Println("暂无虚拟模型路由，运行 cops model add <名称> --provider <供应商> --model <真实模型> 添加。")
			return nil
		}
		ctxOf := func(t config.RouteTarget) string {
			if ctxSrc != nil {
				if n := ctxSrc.TargetContext(t); n > 0 {
					return fmtContextTokens(n)
				}
			}
			return "-"
		}
		rows := [][]string{}
		for _, name := range r.SortedVirtualModels() {
			t := r.VirtualModels[name]
			mark := "  "
			if name == r.DefaultVirtual {
				mark = "★"
			}
			rows = append(rows, []string{mark, name, t.Provider, t.Model, ctxOf(t)})
		}
		if len(rows) > 0 {
			printTable([]string{"", "虚拟模型", "供应商", "真实模型", "上下文"}, rows)
			fmt.Println("\n★ = 默认（钉住名请求路由到它，cops model use 切换）")
		}
		if r.Utility != nil {
			fmt.Printf("\nutility 辅助路由: %s / %s（命中内部 id 如 gpt-5.4-nano）\n", r.Utility.Provider, r.Utility.Model)
			if c := ctxOf(*r.Utility); c != "-" {
				fmt.Printf("  上下文: %s\n", c)
			}
			fmt.Printf("  匹配模式: %s\n", strings.Join(r.EffectiveUtilityPatterns(), "  "))
		}
		return nil
	},
}

var modelAddCmd = &cobra.Command{
	Use:   "add <虚拟名>",
	Short: "添加虚拟模型（映射到某供应商的真实模型）",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		reader := bufio.NewReader(os.Stdin)
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		name = strings.TrimSpace(prompt(reader, "虚拟模型名（如 cops-pro）", name))
		provider, _ := cmd.Flags().GetString("provider")
		provider = strings.TrimSpace(prompt(reader, "供应商名称（cops list 查看）", provider))
		realModel, _ := cmd.Flags().GetString("model")
		realModel = strings.TrimSpace(prompt(reader, "真实模型（该供应商上的模型名）", realModel))
		ctxFlag, _ := cmd.Flags().GetString("context")
		ctxWin := 0
		if strings.TrimSpace(ctxFlag) != "" {
			n, err := config.ParseContextWindow(ctxFlag)
			if err != nil {
				return err
			}
			ctxWin = n
		}

		r, base, online, err := loadRoutingForWrite()
		if err != nil {
			return err
		}
		if _, dup := r.VirtualModels[name]; dup {
			return fmt.Errorf("虚拟模型已存在: %s（cops model remove 后重新添加，或在 Web 管理页编辑）", name)
		}
		// 供应商存在性校验（在线时服务端 PUT 也会再校验一次）。
		if !online {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if _, idx := cfg.Find(provider); idx < 0 {
				return fmt.Errorf("供应商不存在: %s（cops list 查看）", provider)
			}
		}
		if r.VirtualModels == nil {
			r.VirtualModels = map[string]config.RouteTarget{}
		}
		r.VirtualModels[name] = config.RouteTarget{Provider: provider, Model: realModel, ContextWindow: ctxWin}
		if r.DefaultVirtual == "" {
			r.DefaultVirtual = name // 第一个虚拟模型自动成为默认
		}
		if err := saveRouting(base, online, r); err != nil {
			return err
		}
		mode := ""
		if !online {
			mode = "\n   守护进程未运行：下次启动后生效。"
		}
		fmt.Printf("✅ 已添加虚拟模型 %s → %s / %s%s\n", name, provider, realModel, mode)
		if !online {
			fmt.Printf("   切换默认: cops model use %s\n", name)
		} else {
			fmt.Printf("   使用: cops model use %s\n", name)
		}
		return nil
	},
}

var modelRemoveCmd = &cobra.Command{
	Use:   "remove <虚拟名>",
	Short: "删除虚拟模型",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		r, base, online, err := loadRoutingForWrite()
		if err != nil {
			return err
		}
		if _, ok := r.VirtualModels[args[0]]; !ok {
			return fmt.Errorf("虚拟模型不存在: %s（cops model list 查看）", args[0])
		}
		delete(r.VirtualModels, args[0])
		if r.DefaultVirtual == args[0] {
			r.DefaultVirtual = ""
			// 回退到排序后的第一个，避免默认悬空。
			if names := r.SortedVirtualModels(); len(names) > 0 {
				r.DefaultVirtual = names[0]
			}
		}
		if err := saveRouting(base, online, r); err != nil {
			return err
		}
		fmt.Printf("✅ 已删除 %s", args[0])
		if r.DefaultVirtual != "" {
			fmt.Printf("（默认回退到 %s）", r.DefaultVirtual)
		}
		fmt.Println()
		return nil
	},
}

var modelUseCmd = &cobra.Command{
	Use:   "use <虚拟名>",
	Short: "切换默认虚拟模型（守护进程运行中时即时生效，无需重启 Copilot CLI）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if base, ok := daemonAPI(); ok {
			body, _ := json.Marshal(map[string]string{"name": name})
			resp, err := (&http.Client{Timeout: 5 * time.Second}).Post(
				base+"/_cops/api/routing/use", "application/json", bytes.NewReader(body))
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
				msg := out.Error
				if msg == "" {
					msg = fmt.Sprintf("HTTP %d（守护进程可能为旧版本，请用新版 cops 重启托盘）", resp.StatusCode)
				}
				return fmt.Errorf("切换失败: %s", msg)
			}
			fmt.Printf("✅ 默认虚拟模型 → %s —— 即时生效，无需重启 Copilot CLI\n", name)
			return nil
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if _, ok := cfg.Routing.VirtualModels[name]; !ok {
			return fmt.Errorf("虚拟模型不存在: %s（cops model list 查看）", name)
		}
		cfg.Routing.DefaultVirtual = name
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("✅ 默认虚拟模型 → %s。\n   守护进程未运行：下次 cops serve 启动后生效。\n", name)
		return nil
	},
}

func init() {
	modelAddCmd.Flags().String("provider", "", "供应商名称")
	modelAddCmd.Flags().String("model", "", "真实模型名")
	modelAddCmd.Flags().String("context", "", "上下文窗口（如 128k、1.5m，可选；留空回退真实模型配置）")
	modelCmd.AddCommand(modelListCmd, modelAddCmd, modelRemoveCmd, modelUseCmd)
	rootCmd.AddCommand(modelCmd)
}
