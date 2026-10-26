package cli

import (
	"testing"

	"cops/internal/config"
)

func TestMergeNoProxyPreservesExistingAndAddsLoopback(t *testing.T) {
	got := mergeNoProxy("corp.example,localhost", "EXAMPLE.com, corp.example")
	want := "corp.example,localhost,EXAMPLE.com,127.0.0.1,::1"
	if got != want {
		t.Fatalf("mergeNoProxy() = %q, want %q", got, want)
	}
	if !hasLoopbackNoProxy(got) {
		t.Fatalf("hasLoopbackNoProxy(%q) = false", got)
	}
	if hasLoopbackNoProxy("localhost,127.0.0.1") {
		t.Fatal("hasLoopbackNoProxy should require all loopback forms")
	}
}

func TestCopilotEnvVarsMaxPromptTokens(t *testing.T) {
	// 主对话目标上下文已知 → 注入推导值。
	cfg := config.Default()
	cfg.Providers = []config.Provider{{
		Name: "glm", BaseURL: "https://x.example/v1", Model: "m",
		ModelContext: map[string]int{"m": 128000},
	}}
	cfg.Active = "glm"
	vars := copilotEnvVars(cfg)
	if got := vars[config.EnvMaxPromptTokens]; got != "128000" {
		t.Fatalf("%s = %q, want 128000", config.EnvMaxPromptTokens, got)
	}

	// 无上下文信息 → 不写该变量（沿用用户手动值）。
	cfg2 := config.Default()
	cfg2.Providers = []config.Provider{{Name: "glm", BaseURL: "https://x.example/v1", Model: "m"}}
	cfg2.Active = "glm"
	vars2 := copilotEnvVars(cfg2)
	if _, ok := vars2[config.EnvMaxPromptTokens]; ok {
		t.Fatal("上下文未知时不应注入 MAX_PROMPT_TOKENS")
	}
}
