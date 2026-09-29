package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// withTrayStub 替换委托目标，记录调用并隔离全局 trayKeepConsole。
func withTrayStub(t *testing.T, fn func(called *bool, cmd **cobra.Command)) {
	t.Helper()
	called := false
	var gotCmd *cobra.Command
	origRunE, origConsole := trayRunE, trayKeepConsole
	trayRunE = func(cmd *cobra.Command, args []string) error {
		called, gotCmd = true, cmd
		return nil
	}
	defer func() { trayRunE, trayKeepConsole = origRunE, origConsole }()

	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs(nil)
	fn(&called, &gotCmd)
}

func TestBareCopsDelegatesToTray(t *testing.T) {
	withTrayStub(t, func(called *bool, cmd **cobra.Command) {
		rootCmd.SetArgs([]string{})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("裸 cops 应成功执行: %v", err)
		}
		if !*called {
			t.Fatal("裸 cops 应委托 trayCmd.RunE")
		}
		if *cmd != trayCmd {
			t.Fatal("委托目标应为 trayCmd")
		}
	})
}

func TestBareCopsConsoleFlagPropagates(t *testing.T) {
	withTrayStub(t, func(called *bool, _ **cobra.Command) {
		rootCmd.SetArgs([]string{"--console"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("cops --console 应成功执行: %v", err)
		}
		if !*called {
			t.Fatal("cops --console 应委托 trayCmd.RunE")
		}
		if !trayKeepConsole {
			t.Fatal("cops --console 应置位 trayKeepConsole")
		}
	})
}

func TestUnknownCommandStillErrors(t *testing.T) {
	withTrayStub(t, func(called *bool, _ **cobra.Command) {
		rootCmd.SetArgs([]string{"definitely-not-a-command"})
		err := rootCmd.Execute()
		if err == nil {
			t.Fatal("未知命令应报错")
		}
		if !strings.Contains(err.Error(), "unknown command") {
			t.Fatalf("应为 unknown command 错误，实际: %v", err)
		}
		if *called {
			t.Fatal("未知命令不应进入 tray 委托")
		}
	})
}

func TestVersionAndHelpBypassTray(t *testing.T) {
	withTrayStub(t, func(called *bool, _ **cobra.Command) {
		rootCmd.SetArgs([]string{"--version"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("cops --version 应成功: %v", err)
		}
		if *called {
			t.Fatal("--version 不应进入 tray 委托")
		}

		*called = false
		rootCmd.SetArgs([]string{"help"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("cops help 应成功: %v", err)
		}
		if *called {
			t.Fatal("help 不应进入 tray 委托")
		}
	})
}

