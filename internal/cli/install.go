package cli

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"cops/internal/config"
	"cops/internal/winenv"
)

// COPILOT 环境变量名。
const (
	envProviderType    = "COPILOT_PROVIDER_TYPE"
	envProviderBaseURL = "COPILOT_PROVIDER_BASE_URL"
	envProviderAPIKey  = "COPILOT_PROVIDER_API_KEY"
	envModel           = "COPILOT_MODEL"
	dummyAPIKey        = "cops-local"
)

// copilotEnvVars 构建指向本地代理的 COPILOT_* 环境变量集。
func copilotEnvVars(cfg *config.Config) map[string]string {
	return map[string]string{
		envProviderType:    "openai",
		envProviderBaseURL: cfg.PublicURL(),
		envProviderAPIKey:  dummyAPIKey,
		envModel:           cfg.VirtualModel,
	}
}

// copilotEnvNames COPILOT_* 变量名列表。
func copilotEnvNames() []string {
	return []string{envProviderType, envProviderBaseURL, envProviderAPIKey, envModel}
}

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "一键安装：注入 COPILOT_* 环境变量（带备份）+ 设置托盘开机自启",
	Long: `将 Copilot CLI 指向本地 cops 代理：
  1. 注入用户环境变量 COPILOT_PROVIDER_TYPE / BASE_URL / API_KEY / MODEL
     （自动备份原值到 ~/.cops/env-backup.json，cops tray 退出或 uninstall 时恢复）
	  2. 写入平台对应的用户环境配置（新开终端即可生效，已开终端需重开）
	  3. 设置用户级开机自启动 cops tray（托盘常驻 + 启动即注入 + 退出即恢复）

完成后请打开新的终端运行 copilot。`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		model, _ := cmd.Flags().GetString("model")
		if model != "" {
			cfg.VirtualModel = model
			if err := cfg.Save(); err != nil {
				return err
			}
		}

		vars := copilotEnvVars(cfg)
		if err := winenv.InjectEnv(config.EnvBackupPath(), vars); err != nil {
			return fmt.Errorf("注入环境变量失败: %w", err)
		}

		exe, err := os.Executable()
		if err == nil {
			exe, _ = filepath.Abs(exe)
			if err := winenv.SetAutostart(exe, "tray --autostart"); err != nil {
				if errors.Is(err, winenv.ErrUnsupported) {
					fmt.Println("ℹ️ 当前平台不支持开机自启，已跳过")
				} else {
					fmt.Println("⚠ 设置开机自启失败:", err)
				}
			}
		}

		fmt.Println("✅ 安装完成！")
		fmt.Println()
		fmt.Println("已注入用户环境变量（原值已备份，可随时恢复）：")
		for k, v := range vars {
			fmt.Printf("  %s=%s\n", k, v)
		}
		fmt.Println()
		fmt.Println("下一步：")
		if runtime.GOOS == "darwin" {
			fmt.Println("  LaunchAgent 已加载，托盘已启动；以后登录时会自动启动")
		} else if winenv.TraySupported() {
			fmt.Println("  1. 运行 cops tray 启动托盘（本机即刻生效；开机自启已设置）")
		} else {
			fmt.Println("  1. 运行 cops serve 前台启动守护进程（本平台不支持托盘）")
		}
		fmt.Println("  2. 打开一个新的终端（必须新开，已开的终端读不到新环境变量）")
		if cfg.ActiveProvider() == nil {
			fmt.Println("  3. cops add 添加供应商并填入真实 API Key（或托盘菜单打开管理页）")
		} else {
			fmt.Printf("  3. 直接运行 copilot（当前供应商: %s）\n", cfg.Active)
		}
		fmt.Println("  停止: 托盘菜单「退出」或 cops uninstall —— 均自动恢复环境变量")
		return nil
	},
}

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "卸载：恢复 COPILOT_* 环境变量、清理开机自启（保留 ~/.cops 配置与数据）",
	RunE: func(cmd *cobra.Command, args []string) error {
		restored, err := winenv.RestoreEnv(config.EnvBackupPath())
		if err != nil {
			fmt.Println("⚠ 恢复备份失败:", err)
		}
		if !restored {
			// 无备份（如 v0.1 直接写入未备份）：退回删除语义
			for _, name := range copilotEnvNames() {
				if err := winenv.DeleteUserEnv(name); err != nil {
					fmt.Printf("⚠ 删除 %s 失败: %v\n", name, err)
				}
			}
			winenv.BroadcastSettingChange()
		}
		if err := winenv.RemoveAutostart(); err != nil {
			fmt.Println("⚠ 移除自启动失败:", err)
		}
		fmt.Println("✅ 已恢复环境变量并清理自启动。配置与用量数据保留在 ~/.cops/（可手动删除）。")
		fmt.Println("   新开终端后 copilot 将恢复使用 GitHub Copilot 官方认证。")
		return nil
	},
}

var injectCmd = &cobra.Command{
	Use:   "inject",
	Short: "手动注入 COPILOT_* 环境变量（托盘启动时也会自动执行）",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if winenv.IsInjected(config.EnvBackupPath()) {
			fmt.Println("已处于注入状态（备份存在）；如需刷新值请先 cops restore。")
			return nil
		}
		if err := winenv.InjectEnv(config.EnvBackupPath(), copilotEnvVars(cfg)); err != nil {
			return err
		}
		fmt.Println("✅ 已注入 COPILOT_* 环境变量（原值已备份）。新开终端后生效。")
		return nil
	},
}

var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "恢复注入前的 COPILOT_* 环境变量（托盘退出时也会自动执行）",
	RunE: func(cmd *cobra.Command, args []string) error {
		restored, err := winenv.RestoreEnv(config.EnvBackupPath())
		if err != nil {
			return err
		}
		if !restored {
			fmt.Println("当前未处于注入状态（无备份），无需恢复。")
			return nil
		}
		fmt.Println("✅ 已恢复注入前的环境变量。新开终端后 copilot 走官方认证。")
		return nil
	},
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "诊断：检查配置、守护进程、环境变量、自启动与上游连通性",
	RunE: func(cmd *cobra.Command, args []string) error {
		ok := 0
		fail := 0
		check := func(pass bool, label, hint string) {
			if pass {
				fmt.Printf("✅ %s\n", label)
				ok++
			} else {
				fmt.Printf("❌ %s\n    → %s\n", label, hint)
				fail++
			}
		}

		cfg, err := config.Load()
		check(err == nil, "配置文件可读（"+config.Path()+"）", "查看错误信息；必要时删除后重建：cops add")

		if err == nil {
			_, running := daemonAPI()
			check(running, "守护进程运行中（"+cfg.Listen+"）", "启动: cops serve（或重启机器走自启动）")

			if p := cfg.ActiveProvider(); p == nil {
				check(false, "已激活供应商", "运行 cops add 添加后 cops switch <名称> 激活")
			} else {
				check(true, fmt.Sprintf("已激活供应商: %s（模型 %s）", p.Name, p.Model), "")
				// 上游连通性
				client := &http.Client{Timeout: 15 * time.Second}
				req, _ := http.NewRequest("GET", p.BaseURL+"/models", nil)
				if p.APIKey != "" {
					req.Header.Set("Authorization", "Bearer "+p.APIKey)
				}
				resp, err := client.Do(req)
				if err != nil {
					check(false, "上游连通性（"+p.BaseURL+"）", err.Error())
				} else {
					resp.Body.Close()
					check(resp.StatusCode < 500, fmt.Sprintf("上游连通性（HTTP %d）", resp.StatusCode), "检查 API Key 或 Base URL 是否正确")
				}
			}

			// 路由配置（v0.2）：仅在配置了路由时检查，避免对旧配置产生噪音。
			if len(cfg.Routing.VirtualModels) > 0 || cfg.Routing.Utility != nil {
				if rerr := cfg.ValidateRouting(); rerr != nil {
					check(false, "路由配置有效", rerr.Error())
				} else {
					dv := cfg.Routing.DefaultVirtual
					if dv == "" {
						dv = "（未设置，钉住名走激活供应商默认模型）"
					}
					check(true, fmt.Sprintf("路由配置有效（%d 个虚拟模型，默认 %s）", len(cfg.Routing.VirtualModels), dv), "")
				}
				if _, clash := cfg.Routing.VirtualModels[cfg.VirtualModel]; clash {
					fmt.Printf("ℹ️  注意: 钉住名 %s 同时是虚拟模型表键，该名按表内路由，cops model use 切换不影响它。\n", cfg.VirtualModel)
				}
			}
		}

		injected := winenv.IsInjected(config.EnvBackupPath())
		if injected {
			if b, err := winenv.ReadBackup(config.EnvBackupPath()); err == nil {
				n := 0
				for _, e := range b.Vars {
					if e.Exists {
						n++
					}
				}
				check(true, fmt.Sprintf("注入状态: 已注入（备份原值 %d 项，cops tray 退出时自动恢复）", n), "")
			}
		} else {
			check(true, "注入状态: 未注入（cops tray 启动时会自动注入）", "")
		}

		for _, name := range copilotEnvNames() {
			v, exists := winenv.GetUserEnv(name)
			expected := ""
			if err == nil {
				switch name {
				case envProviderType:
					expected = "openai"
				case envProviderBaseURL:
					expected = cfg.PublicURL()
				case envProviderAPIKey:
					expected = dummyAPIKey
				case envModel:
					expected = cfg.VirtualModel
				}
			}
			// 已注入时必须匹配期望值；未注入时（已恢复）缺省视为正常。
			pass := exists && (expected == "" || v == expected)
			if !injected && !exists {
				pass = true
			}
			check(pass,
				fmt.Sprintf("环境变量 %s=%s", name, v),
				"运行 cops inject 或 cops tray 注入；新开终端后生效")
		}

		if winenv.TraySupported() {
			autoCmd, has := winenv.GetAutostart()
			check(has, "开机自启动已设置（cops tray）", "运行 cops install")
			if has {
				fmt.Printf("   %s\n", autoCmd)
			}
		} else {
			fmt.Println("⏭ 开机自启动: 本平台暂不支持")
		}

		fmt.Printf("\n结果: %d 项通过, %d 项失败\n", ok, fail)
		if fail > 0 {
			os.Exit(1)
		}
		return nil
	},
}

func init() {
	installCmd.Flags().String("model", "", "写入 COPILOT_MODEL 的虚拟模型名（默认 cops-active）")
	rootCmd.AddCommand(installCmd, uninstallCmd, injectCmd, restoreCmd, doctorCmd)
}
