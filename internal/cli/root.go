// Package cli 定义 cops 的全部子命令。
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version 版本号（构建时可用 -ldflags 覆盖）。
var Version = "0.1.0"

var rootCmd = &cobra.Command{
	Use:   "cops",
	Short: "Copilot CLI BYOK 供应商切换代理（cops = COpilot Provider Switch）",
	Long: `cops 在本地运行一个 OpenAI 兼容反向代理，Copilot CLI 通过
COPILOT_PROVIDER_* 环境变量固定指向它；切换模型供应商只需一条命令，
无需重启 Copilot CLI，真实 API Key 也无需写入 shell 环境。

快速上手:
  cops install            # 写入 COPILOT_* 环境变量 + 设置开机自启
  cops add glm --url ...  # 添加供应商
  cops switch glm         # 一键切换
  cops open               # 打开 Web 管理页`,
	SilenceUsage:  true,
	SilenceErrors: true,
	Version:       Version,
}

// Execute 程序入口。
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}
