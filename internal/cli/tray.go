package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"cops/internal/config"
	"cops/internal/tray"
	"cops/internal/winenv"
)

var trayKeepConsole bool

var trayCmd = &cobra.Command{
	Use:   "tray",
	Short: "托盘常驻模式：启动即注入环境变量，退出即恢复并停止代理",
	Long: `cops tray 同时承担托盘 UI 与代理守护进程：
  - 启动时自动注入 COPILOT_* 环境变量（原值备份，可恢复）
  - 托盘菜单：供应商一键切换 / 打开管理页 / 停用·启用注入 / 退出
  - 退出时自动恢复环境变量并停止代理（"停止即还原"）
  - 若检测到已有守护进程在运行，则以附着模式仅提供托盘 UI`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// 0a. 平台降级：托盘尚未在该平台启用（如 macOS 第一阶段），
		//     提示替代方案后干净退出（裸 cops 委托到此，同样适用）。
		if !winenv.TraySupported() {
			fmt.Println("当前平台暂不支持托盘模式（规划中）。")
			fmt.Println("替代方案：cops serve 前台运行守护进程，配合 Web 管理页使用；")
			fmt.Println("环境变量注入可用 cops install / cops inject（新开终端生效）。")
			return nil
		}

		// 0b. 双击启动（控制台仅本进程）时自我重启为无控制台的分离进程：
		//    原进程随即正常退出使控制台窗口干净关闭（Windows Terminal 下
		//    detach 会残留 "[process exited]" 黑窗），托盘由分离进程接管。
		if !trayKeepConsole && winenv.SoleConsole() {
			if err := winenv.RelaunchDetached("tray"); err == nil {
				return nil
			}
			// 重启失败则继续原路径，保证功能可用
		}

		cfg, err := config.Load()
		if err != nil {
			return err
		}

		// 1. 启动即注入（幂等：已有备份则保留最早原值）
		if err := winenv.InjectEnv(config.EnvBackupPath(), copilotEnvVars(cfg)); err != nil {
			return fmt.Errorf("注入环境变量失败: %w", err)
		}

		// 2. 附着 or 嵌入
		baseURL, attached := daemonAPI()
		var d *daemon
		if attached {
			log.Printf("检测到已运行的守护进程（%s），托盘以附着模式运行", baseURL)
		} else {
			d, err = newDaemon(cfg, true) // 日志写 daemon.log
			if err != nil {
				_, _ = winenv.RestoreEnv(config.EnvBackupPath())
				return err
			}
			go func() {
				if err := d.serve(); err != nil {
					log.Printf("守护进程退出: %v", err)
				}
			}()
		}

		// 3. 隐藏控制台（--console 保留便于调试）
		if !trayKeepConsole {
			winenv.FreeConsole()
		}

		// 4. 状态与动作
		stateFn := func() tray.State { return trayState(d, baseURL) }
		client := &http.Client{Timeout: 3 * time.Second}

		switchTo := func(name string) error {
			if d != nil {
				_, err := d.admin.Switch(name, "")
				return err
			}
			body, _ := json.Marshal(map[string]string{"name": name})
			resp, err := client.Post(baseURL+"/_cops/api/switch", "application/json", bytes.NewReader(body))
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				var out struct {
					Error string `json:"error"`
				}
				_ = json.NewDecoder(resp.Body).Decode(&out)
				return fmt.Errorf("%s", out.Error)
			}
			return nil
		}

		useModel := func(name string) error {
			if d != nil {
				_, err := d.admin.UseModel(name)
				return err
			}
			body, _ := json.Marshal(map[string]string{"name": name})
			resp, err := client.Post(baseURL+"/_cops/api/routing/use", "application/json", bytes.NewReader(body))
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				var out struct {
					Error string `json:"error"`
				}
				_ = json.NewDecoder(resp.Body).Decode(&out)
				return fmt.Errorf("%s", out.Error)
			}
			return nil
		}

		openAdmin := func() {
			url := baseURL
			if url == "" {
				url = baseURLOf(cfg)
			}
			if err := openBrowser(url + "/_cops/"); err != nil {
				log.Printf("打开管理页失败: %v", err)
			}
		}

		toggleInject := func() error {
			path := config.EnvBackupPath()
			if winenv.IsInjected(path) {
				_, err := winenv.RestoreEnv(path)
				return err
			}
			// 重新加载配置，取最新监听地址/虚拟模型名
			c, err := config.Load()
			if err != nil {
				return err
			}
			return winenv.InjectEnv(path, copilotEnvVars(c))
		}

		var quitOnce sync.Once
		quit := func() {
			quitOnce.Do(func() {
				log.Printf("托盘退出：恢复环境变量并停止代理")
				if _, err := winenv.RestoreEnv(config.EnvBackupPath()); err != nil {
					log.Printf("恢复环境变量失败: %v", err)
				}
				if d != nil {
					d.stopDaemon()
				} else {
					resp, err := client.Post(baseURL+"/_cops/api/shutdown", "application/json", nil)
					if err == nil {
						_ = resp.Body.Close()
					}
				}
			})
		}

		// 5. 托盘主循环（阻塞；quit 后 Run 返回）
		tray.Run(stateFn, tray.Actions{
			Switch:       switchTo,
			UseModel:     useModel,
			OpenAdmin:    openAdmin,
			ToggleInject: toggleInject,
			Quit:         quit,
		})
		quit() // 兜底：onExit 路径未触发 quit 时确保清理
		return nil
	},
}

// trayState 获取托盘状态：嵌入模式直读内存；附着模式轮询管理 API。
func trayState(d *daemon, baseURL string) tray.State {
	if d != nil {
		cfg := d.cfgPtr.Load()
		st := tray.State{
			Active:         cfg.Active,
			Injected:       winenv.IsInjected(config.EnvBackupPath()),
			Providers:      make([]tray.ProviderInfo, 0, len(cfg.Providers)),
			DefaultVirtual: cfg.Routing.DefaultVirtual,
		}
		for _, p := range cfg.Providers {
			st.Providers = append(st.Providers, tray.ProviderInfo{Name: p.Name, Model: p.Model})
		}
		for _, name := range cfg.Routing.SortedVirtualModels() {
			t := cfg.Routing.VirtualModels[name]
			st.VirtualModels = append(st.VirtualModels, tray.ModelInfo{Name: name, Target: t.Provider + "/" + t.Model})
		}
		return st
	}

	client := &http.Client{Timeout: 2 * time.Second}
	st := tray.State{Injected: winenv.IsInjected(config.EnvBackupPath())}
	if resp, err := client.Get(baseURL + "/_cops/api/providers"); err == nil {
		defer resp.Body.Close()
		var out struct {
			Providers []struct {
				Name   string   `json:"name"`
				Model  string   `json:"model"`
				Models []string `json:"models"`
			} `json:"providers"`
			Active string `json:"active"`
		}
		if json.NewDecoder(resp.Body).Decode(&out) == nil {
			st.Active = out.Active
			for _, p := range out.Providers {
				st.Providers = append(st.Providers, tray.ProviderInfo{Name: p.Name, Model: p.Model})
			}
		}
	}
	if resp, err := client.Get(baseURL + "/_cops/api/routing"); err == nil {
		defer resp.Body.Close()
		var out struct {
			Routing struct {
				DefaultVirtual string                        `json:"defaultVirtual"`
				VirtualModels  map[string]config.RouteTarget `json:"virtualModels"`
			} `json:"routing"`
		}
		if json.NewDecoder(resp.Body).Decode(&out) == nil {
			st.DefaultVirtual = out.Routing.DefaultVirtual
			names := make([]string, 0, len(out.Routing.VirtualModels))
			for name := range out.Routing.VirtualModels {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				t := out.Routing.VirtualModels[name]
				st.VirtualModels = append(st.VirtualModels, tray.ModelInfo{Name: name, Target: t.Provider + "/" + t.Model})
			}
		}
	}
	return st
}

func init() {
	trayCmd.Flags().BoolVar(&trayKeepConsole, "console", false, "保留控制台窗口（调试用）")
	// 裸 cops（rootCmd）同名 flag，与 cops tray --console 等效。
	rootCmd.Flags().BoolVar(&trayKeepConsole, "console", false, "保留控制台窗口（调试用）")
	// 占位 flag：开机自启命令固定携带 --autostart 便于辨识，行为与默认一致。
	trayCmd.Flags().Bool("autostart", false, "由开机自启动项调用（行为与默认一致）")
	rootCmd.AddCommand(trayCmd)
}
