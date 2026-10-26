package config

import (
	"strings"
	"testing"
)

func testConfig() *Config {
	c := Default()
	c.Providers = []Provider{
		{Name: "glm", BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-4.7"},
		{Name: "deepseek", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat"},
		{Name: "ollama", BaseURL: "http://localhost:11434/v1", Model: "qwen3:32b"},
	}
	c.Active = "glm"
	return c
}

func TestMatchesUtility(t *testing.T) {
	r := &RoutingConfig{Utility: &RouteTarget{Provider: "glm", Model: "glm-4-flash"}}
	cases := []struct {
		model string
		want  bool
	}{
		{"gpt-5.4-nano", true},
		{"claude-haiku-mini", true},
		{"gemini-2.5-fast", true},
		{"gpt-5.4", false},
		{"cops-active", false},
		{"", false},
		{"deepseek-chat", false},
	}
	for _, tc := range cases {
		if got := r.MatchesUtility(tc.model); got != tc.want {
			t.Errorf("MatchesUtility(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
	// 未配置 Utility 目标时一律不命中。
	r2 := &RoutingConfig{}
	if r2.MatchesUtility("gpt-5.4-nano") {
		t.Error("no utility target should never match")
	}
	// 自定义模式覆盖内置默认。
	r.UtilityPatterns = []string{"*tiny*"}
	if !r.MatchesUtility("llama-tiny") || r.MatchesUtility("gpt-5.4-nano") {
		t.Error("custom patterns should replace defaults")
	}
}

func TestValidateRouting(t *testing.T) {
	c := testConfig()
	c.Routing = RoutingConfig{
		VirtualModels: map[string]RouteTarget{
			"cops-pro":   {Provider: "glm", Model: "glm-4.7"},
			"cops-flash": {Provider: "deepseek", Model: "deepseek-chat"},
		},
		DefaultVirtual: "cops-pro",
		Utility:        &RouteTarget{Provider: "ollama", Model: "qwen3:8b"},
	}
	if err := c.ValidateRouting(); err != nil {
		t.Fatalf("valid routing rejected: %v", err)
	}

	errCases := []struct {
		name    string
		mutate  func(*Config)
		want    string
	}{
		{"missing provider", func(c *Config) { c.Routing.VirtualModels["bad"] = RouteTarget{Provider: "nope", Model: "x"} }, "不存在"},
		{"empty model", func(c *Config) { c.Routing.VirtualModels["bad"] = RouteTarget{Provider: "glm", Model: " "} }, "未指定模型"},
		{"default not in table", func(c *Config) { c.Routing.DefaultVirtual = "ghost" }, "不在虚拟模型表"},
		{"utility missing provider", func(c *Config) { c.Routing.Utility = &RouteTarget{Provider: "ghost", Model: "x"} }, "不存在"},
		{"bad pattern", func(c *Config) { c.Routing.UtilityPatterns = []string{"[bad"} }, "不合法"},
	}
	for _, tc := range errCases {
		c2 := testConfig()
		c2.Routing = RoutingConfig{
			VirtualModels: map[string]RouteTarget{"a": {Provider: "glm", Model: "m"}},
			Utility:       &RouteTarget{Provider: "glm", Model: "m"},
		}
		tc.mutate(c2)
		err := c2.ValidateRouting()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want contains %q", tc.name, err, tc.want)
		}
	}
}

func TestRoutingRoundtripAndNormalize(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir()); t.Setenv("HOME", t.TempDir())
	c := testConfig()
	c.Routing = RoutingConfig{
		VirtualModels: map[string]RouteTarget{
			"cops-pro ": {Provider: " glm ", Model: " glm-4.7 "},
		},
		DefaultVirtual: " cops-pro ",
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Routing.DefaultVirtual != "cops-pro" {
		t.Fatalf("DefaultVirtual = %q, want trimmed", got.Routing.DefaultVirtual)
	}
	if t2, ok := got.Routing.VirtualModels["cops-pro"]; !ok || t2.Provider != "glm" || t2.Model != "glm-4.7" {
		t.Fatalf("virtual entry mismatch: %+v", got.Routing.VirtualModels)
	}
	// 旧配置（无 routing 字段）加载后为零值，校验通过。
	t.Setenv("USERPROFILE", t.TempDir()); t.Setenv("HOME", t.TempDir())
	c2 := testConfig()
	c2.Save()
	got2, _ := Load()
	if len(got2.Routing.VirtualModels) != 0 || got2.Routing.Utility != nil {
		t.Fatal("legacy config should have zero routing")
	}
	if err := got2.ValidateRouting(); err != nil {
		t.Fatalf("zero routing should validate: %v", err)
	}
}

func TestTargetContext(t *testing.T) {
	c := testConfig()
	c.Providers[0].ModelContext = map[string]int{"glm-4.7": 128000}
	// 覆盖值优先。
	if got := c.TargetContext(RouteTarget{Provider: "glm", Model: "glm-4.7", ContextWindow: 200000}); got != 200000 {
		t.Errorf("override = %d, want 200000", got)
	}
	// 无覆盖时回退真实模型值。
	if got := c.TargetContext(RouteTarget{Provider: "glm", Model: "glm-4.7"}); got != 128000 {
		t.Errorf("fallback = %d, want 128000", got)
	}
	// 真实模型未配置 → 0。
	if got := c.TargetContext(RouteTarget{Provider: "deepseek", Model: "deepseek-chat"}); got != 0 {
		t.Errorf("unknown = %d, want 0", got)
	}
}

func TestValidateRoutingNegativeContext(t *testing.T) {
	c := testConfig()
	c.Routing = RoutingConfig{
		VirtualModels: map[string]RouteTarget{"a": {Provider: "glm", Model: "m", ContextWindow: -1}},
	}
	if err := c.ValidateRouting(); err == nil || !strings.Contains(err.Error(), "负数") {
		t.Fatalf("negative contextWindow should be rejected, got %v", err)
	}
}

func TestNormalizeUtilityEmptyTarget(t *testing.T) {
	r := &RoutingConfig{Utility: &RouteTarget{}}
	r.Normalize()
	if r.Utility != nil {
		t.Fatal("empty utility target should be normalized to nil")
	}
}

func TestSortedVirtualModels(t *testing.T) {
	r := &RoutingConfig{VirtualModels: map[string]RouteTarget{
		"b": {Provider: "p", Model: "m"}, "a": {Provider: "p", Model: "m"},
	}}
	got := r.SortedVirtualModels()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got %v", got)
	}
}

func TestMaxPromptTokensForBYOK(t *testing.T) {
	// 无路由、无激活供应商 → 0（不注入）。
	c := Default()
	if got := c.MaxPromptTokensForBYOK(); got != 0 {
		t.Fatalf("空配置 = %d, want 0", got)
	}

	// 无路由：主对话目标 = 激活供应商默认模型，回退其 ModelContext。
	c = testConfig()
	c.Providers[0].ModelContext = map[string]int{"glm-4.7": 128000}
	if got := c.MaxPromptTokensForBYOK(); got != 128000 {
		t.Fatalf("激活供应商回退 = %d, want 128000", got)
	}

	// 供应商覆盖值优先于一切推导。
	c.Providers[0].MaxPromptTokens = 200000
	if got := c.MaxPromptTokensForBYOK(); got != 200000 {
		t.Fatalf("供应商覆盖 = %d, want 200000", got)
	}

	// 默认虚拟表项优先于激活供应商；虚拟 ContextWindow 覆盖真实模型值。
	c = testConfig()
	c.Providers[1].ModelContext = map[string]int{"deepseek-chat": 64000}
	c.Routing = RoutingConfig{
		VirtualModels:   map[string]RouteTarget{"cops-pro": {Provider: "deepseek", Model: "deepseek-chat", ContextWindow: 96000}},
		DefaultVirtual:  "cops-pro",
	}
	if got := c.MaxPromptTokensForBYOK(); got != 96000 {
		t.Fatalf("虚拟覆盖 = %d, want 96000", got)
	}
	// 虚拟表项无 ContextWindow 覆盖 → 回退真实模型值。
	c.Routing.VirtualModels["cops-pro"] = RouteTarget{Provider: "deepseek", Model: "deepseek-chat"}
	if got := c.MaxPromptTokensForBYOK(); got != 64000 {
		t.Fatalf("虚拟回退 = %d, want 64000", got)
	}
	// 目标供应商覆盖值压过虚拟表项上下文。
	c.Providers[1].MaxPromptTokens = 300000
	if got := c.MaxPromptTokensForBYOK(); got != 300000 {
		t.Fatalf("覆盖压过虚拟 = %d, want 300000", got)
	}

	// 目标真实模型无上下文配置且无覆盖 → 0。
	c = testConfig()
	c.Routing = RoutingConfig{
		VirtualModels:  map[string]RouteTarget{"cops-pro": {Provider: "glm", Model: "glm-4.7"}},
		DefaultVirtual: "cops-pro",
	}
	if got := c.MaxPromptTokensForBYOK(); got != 0 {
		t.Fatalf("无任何上下文 = %d, want 0", got)
	}
}

func TestValidateNegativeMaxPromptTokens(t *testing.T) {
	c := testConfig()
	c.Providers[0].MaxPromptTokens = -1
	if err := c.Providers[0].Validate(); err == nil || !strings.Contains(err.Error(), "负数") {
		t.Fatalf("负数 maxPromptTokens 应被拒绝, got %v", err)
	}
}
