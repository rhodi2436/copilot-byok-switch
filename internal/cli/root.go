// Package cli 定义 cops 的全部子命令。
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version 版本号（构建时可用 -ldflags 覆盖）。
var Version = "0.2.0"

// trayRunE 裸 cops 的委托目标；包级变量便于测试替换。
var trayRunE = func(cmd *cobra.Command, args []string) error {
	return trayCmd.RunE(cmd, args)
}

var rootCmd = &cobra.Command{
	Use:   "cops",
	Short: "Copilot CLI BYOK 供应商切换代理（cops = COpilot Provider Switch）",
	Long: `cops 在本地运行一个 OpenAI 兼容反向代理，Copilot CLI 通过
COPILOT_PROVIDER_* 环境变量固定指向它；切换模型供应商只需一条命令，
无需重启 Copilot CLI，真实 API Key 也无需写入 shell 环境。

快速上手:
  cops（或双击 cops.exe）  # 无参数默认进托盘 = cops tray：守护进程 + 一键切换
  cops install            # 写入 COPILOT_* 环境变量 + 设置开机自启
  cops add glm --url ...  # 添加供应商
  cops switch glm         # 一键切换
  cops open               # 打开 Web 管理页`,
	SilenceUsage:  true,
	SilenceErrors: true,
	Version:       Version,
	// 裸 cops（无子命令）委托 tray：双击 exe 即启动守护进程 + 托盘。
	RunE: func(cmd *cobra.Command, args []string) error {
		return trayRunE(trayCmd, args)
	},
}

// Execute 程序入口。
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}
