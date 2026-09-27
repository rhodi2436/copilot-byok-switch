package cli

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "一键安装：写入 COPILOT_* 用户环境变量 + 设置开机自启",
	Long: `将 Copilot CLI 指向本地 cops 代理：
  1. 写入用户环境变量 COPILOT_PROVIDER_TYPE / BASE_URL / API_KEY / MODEL
  2. 广播 WM_SETTINGCHANGE（新开终端即可生效，已开终端需重开）
  3. 写入 HKCU Run 开机自启动 cops serve --headless

完成后请打开新的终端运行 copilot。`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		// 保留已有配置，仅在无供应商时给出提示。
		baseURL := cfg.PublicURL()
		model, _ := cmd.Flags().GetString("model")
		if model != "" {
			cfg.VirtualModel = model
			if err := cfg.Save(); err != nil {
				return err
			}
		}

		vars := map[string]string{
			envProviderType:    "openai",
			envProviderBaseURL: baseURL,
			envProviderAPIKey:  dummyAPIKey,
			envModel:           cfg.VirtualModel,
		}
		for k, v := range vars {
			if err := winenv.SetUserEnv(k, v); err != nil {
				return fmt.Errorf("写入环境变量 %s 失败: %w", k, err)
			}
		}
		winenv.BroadcastSettingChange()

		exe, err := os.Executable()
		if err == nil {
			exe, _ = filepath.Abs(exe)
			if err := winenv.SetAutostart(exe); err != nil {
				fmt.Println("⚠ 设置开机自启失败:", err)
			}
		}

		fmt.Println("✅ 安装完成！")
		fmt.Println()
		fmt.Println("已写入用户环境变量：")
		for k, v := range vars {
			fmt.Printf("  %s=%s\n", k, v)
		}
		fmt.Println()
		fmt.Println("下一步：")
		fmt.Println("  1. 打开一个新的终端（必须新开，已开的终端读不到新环境变量）")
		if cfg.ActiveProvider() == nil {
			fmt.Println("  2. cops add 添加供应商并填入真实 API Key（或访问 Web 管理页配置）")
			fmt.Println("  3. cops switch <名称> 激活")
		} else {
			fmt.Printf("  2. 直接运行 copilot（当前供应商: %s）\n", cfg.Active)
		}
		fmt.Println("  管理页: cops open")
		return nil
	},
}

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "卸载：清理 COPILOT_* 环境变量与开机自启（保留 ~/.cops 配置与数据）",
	RunE: func(cmd *cobra.Command, args []string) error {
		for _, name := range []string{envProviderType, envProviderBaseURL, envProviderAPIKey, envModel} {
			if err := winenv.DeleteUserEnv(name); err != nil {
				fmt.Printf("⚠ 删除 %s 失败: %v\n", name, err)
			}
		}
		winenv.BroadcastSettingChange()
		if err := winenv.RemoveAutostart(); err != nil {
			fmt.Println("⚠ 移除自启动失败:", err)
		}
		fmt.Println("✅ 已清理环境变量与自启动。配置与用量数据保留在 ~/.cops/（可手动删除）。")
		fmt.Println("   新开终端后 copilot 将恢复使用 GitHub Copilot 官方认证。")
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
		}

		for _, name := range []string{envProviderType, envProviderBaseURL, envProviderAPIKey, envModel} {
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
			check(exists && (expected == "" || v == expected),
				fmt.Sprintf("环境变量 %s=%s", name, v),
				"运行 cops install 重新写入；新开终端后生效")
		}

		autoCmd, has := winenv.GetAutostart()
		check(has, "开机自启动已设置", "运行 cops install")
		if has {
			fmt.Printf("   %s\n", autoCmd)
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
	rootCmd.AddCommand(installCmd, uninstallCmd, doctorCmd)
}
