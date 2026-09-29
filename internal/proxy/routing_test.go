package proxy

// v0.2 路由专项测试：utility 命中 / 虚拟命中（跨供应商）/ 钉住名 /
// 未知 id 兜底 / stripSampling / /v1/models 虚拟名标注。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"cops/internal/config"
	"cops/internal/reqlog"
	"cops/internal/stats"
)

// upstreamRecorder 记录上游收到的请求。
type upstreamRecorder struct {
	mu    sync.Mutex
	model string
	body  map[string]any
	auth  string
	hits  int
}

func (u *upstreamRecorder) record(r *http.Request) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.hits++
	u.auth = r.Header.Get("Authorization")
	u.body = nil
	if data, err := io.ReadAll(r.Body); err == nil {
		_ = json.Unmarshal(data, &u.body)
	}
	if u.body != nil {
		u.model, _ = u.body["model"].(string)
	}
}

func (u *upstreamRecorder) snapshot() (model string, hasTemp, hasTopP bool, hits int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	_, hasTemp = u.body["temperature"]
	_, hasTopP = u.body["top_p"]
	return u.model, hasTemp, hasTopP, u.hits
}

func (u *upstreamRecorder) authHeader() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.auth
}

func (u *upstreamRecorder) newServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":5}}`))
	}))
	t.Cleanup(ts.Close)
	return ts
}

// newRoutedProxy 构建双上游（a=激活，b=备用）代理。
func newRoutedProxy(t *testing.T, mutate func(c *config.Config)) (*httptest.Server, *upstreamRecorder, *upstreamRecorder) {
	t.Helper()
	recA, recB := &upstreamRecorder{}, &upstreamRecorder{}
	upA, upB := recA.newServer(t), recB.newServer(t)

	c := config.Default()
	c.Active = "a"
	c.VirtualModel = "cops-active"
	c.Providers = []config.Provider{
		{Name: "a", BaseURL: upA.URL, APIKey: "key-aaaaaaaaaa", Model: "a-model"},
		{Name: "b", BaseURL: upB.URL, APIKey: "key-bbbbbbbbbb", Model: "b-model",
			Prices: map[string]config.ModelPrice{"b-flash": {InputPerMillionTokens: 1, OutputPerMillionTokens: 1}}},
	}
	if mutate != nil {
		mutate(c)
	}
	var ptr atomic.Pointer[config.Config]
	ptr.Store(c)
	st := stats.New(filepath.Join(t.TempDir(), "stats.json"))
	lg, _ := reqlog.Open(t.TempDir(), reqlog.Options{Enabled: false})
	t.Cleanup(func() { lg.Close() })
	ts := httptest.NewServer(New(&ptr, st, lg))
	t.Cleanup(ts.Close)
	return ts, recA, recB
}

func postChat(t *testing.T, ts *httptest.Server, body string) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestRouteVirtualModelCrossProvider(t *testing.T) {
	ts, recA, recB := newRoutedProxy(t, func(c *config.Config) {
		c.Routing.VirtualModels = map[string]config.RouteTarget{
			"cops-pro":   {Provider: "a", Model: "a-big"},
			"cops-flash": {Provider: "b", Model: "b-flash"},
		}
	})
	postChat(t, ts, `{"model":"cops-flash","messages":[]}`)
	if model, _, _, hits := recB.snapshot(); hits != 1 || model != "b-flash" {
		t.Fatalf("上游 b 收到 model=%q hits=%d, want b-flash/1", model, hits)
	}
	if _, _, _, hits := recA.snapshot(); hits != 0 {
		t.Fatalf("上游 a 不应收到请求, hits=%d", hits)
	}
	if !strings.HasPrefix(recB.authHeader(), "Bearer key-b") {
		t.Fatalf("auth = %q, want provider b 的密钥", recB.auth)
	}
}

func TestRouteUtilityInternalID(t *testing.T) {
	ts, recA, recB := newRoutedProxy(t, func(c *config.Config) {
		c.Routing.Utility = &config.RouteTarget{Provider: "b", Model: "b-flash"}
		// 不配置 UtilityPatterns，验证内置默认 *nano* / *-mini* / *fast* 生效。
	})
	postChat(t, ts, `{"model":"gpt-5.4-nano","messages":[]}`)
	if model, _, _, hits := recB.snapshot(); hits != 1 || model != "b-flash" {
		t.Fatalf("utility 未生效: model=%q hits=%d", model, hits)
	}
	// 未命中模式（非 nano/mini/fast）→ 走激活供应商默认。
	postChat(t, ts, `{"model":"gpt-5.4","messages":[]}`)
	if model, _, _, _ := recA.snapshot(); model != "a-model" {
		t.Fatalf("非 utility id 应回落默认, got %q", model)
	}
}

func TestRoutePinnedName(t *testing.T) {
	ts, recA, recB := newRoutedProxy(t, func(c *config.Config) {
		c.Routing.VirtualModels = map[string]config.RouteTarget{
			"cops-pro": {Provider: "b", Model: "b-pro"},
		}
		c.Routing.DefaultVirtual = "cops-pro"
	})
	// 钉住名（COPILOT_MODEL 值）→ DefaultVirtual 表项。
	postChat(t, ts, `{"model":"cops-active","messages":[]}`)
	if model, _, _, hits := recB.snapshot(); hits != 1 || model != "b-pro" {
		t.Fatalf("pinned 未生效: model=%q hits=%d", model, hits)
	}
	// 未知 id → 激活供应商兜底，永不 404。
	postChat(t, ts, `{"model":"whatever-unknown","messages":[]}`)
	if model, _, _, _ := recA.snapshot(); model != "a-model" {
		t.Fatalf("未知 id 应兜底到 a-model, got %q", model)
	}
}

func TestRouteVirtualOverridesPinned(t *testing.T) {
	// cfg.VirtualModel 本身是虚拟表键时，规则 2（virtual）优先于规则 3（pinned）。
	ts, recA, recB := newRoutedProxy(t, func(c *config.Config) {
		c.VirtualModel = "cops-active"
		c.Routing.VirtualModels = map[string]config.RouteTarget{
			"cops-active": {Provider: "a", Model: "a-direct"},
			"cops-pro":    {Provider: "b", Model: "b-pro"},
		}
		c.Routing.DefaultVirtual = "cops-pro"
	})
	postChat(t, ts, `{"model":"cops-active","messages":[]}`)
	if model, _, _, hits := recA.snapshot(); hits != 1 || model != "a-direct" {
		t.Fatalf("virtual 应优先于 pinned: model=%q hits=%d", model, hits)
	}
	if _, _, _, hits := recB.snapshot(); hits != 0 {
		t.Fatalf("上游 b 不应收到请求")
	}
}

func TestRouteUtilityPriorityOverVirtual(t *testing.T) {
	ts, _, recB := newRoutedProxy(t, func(c *config.Config) {
		c.Routing.VirtualModels = map[string]config.RouteTarget{
			"cops-nano": {Provider: "a", Model: "a-model"},
		}
		c.Routing.Utility = &config.RouteTarget{Provider: "b", Model: "b-flash"}
	})
	postChat(t, ts, `{"model":"cops-nano","messages":[]}`)
	if model, _, _, _ := recB.snapshot(); model != "b-flash" {
		t.Fatalf("utility 应优先于 virtual, got %q", model)
	}
}

func TestStripSampling(t *testing.T) {
	ts, recA, _ := newRoutedProxy(t, func(c *config.Config) {
		c.Providers[0].StripSampling = true
	})
	postChat(t, ts, `{"model":"x","temperature":0,"top_p":0.95,"frequency_penalty":0,"presence_penalty":0,"messages":[]}`)
	if _, hasTemp, hasTopP, _ := recA.snapshot(); hasTemp || hasTopP {
		t.Fatalf("StripSampling 未生效: temp=%v top_p=%v", hasTemp, hasTopP)
	}
	// 默认（关）不剥离。
	ts2, recA2, _ := newRoutedProxy(t, nil)
	postChat(t, ts2, `{"model":"x","temperature":0,"top_p":0.95,"messages":[]}`)
	if _, hasTemp, hasTopP, _ := recA2.snapshot(); !hasTemp || !hasTopP {
		t.Fatalf("默认应保留采样参数: temp=%v top_p=%v", hasTemp, hasTopP)
	}
}

func TestModelsListWithVirtualNames(t *testing.T) {
	ts, _, _ := newRoutedProxy(t, func(c *config.Config) {
		c.Providers[0].Models = []string{"a-lite"}
		c.Routing.VirtualModels = map[string]config.RouteTarget{
			"cops-pro":   {Provider: "b", Model: "b-pro"},
			"cops-flash": {Provider: "b", Model: "b-flash"},
		}
		c.Routing.DefaultVirtual = "cops-flash"
	})
	resp, err := http.Get(ts.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID         string `json:"id"`
			CopsTarget string `json:"cops_target"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Data) != 5 {
		t.Fatalf("models = %+v", out.Data)
	}
	byID := map[string]string{}
	for _, d := range out.Data {
		byID[d.ID] = d.CopsTarget
	}
	if byID["cops-active"] != "b/b-flash" {
		t.Fatalf("钉住名标注 = %q, want b/b-flash（DefaultVirtual 去向）", byID["cops-active"])
	}
	if byID["cops-pro"] != "b/b-pro" || byID["cops-flash"] != "b/b-flash" {
		t.Fatalf("虚拟名标注错误: %+v", byID)
	}
	if _, ok := byID["a-lite"]; !ok {
		t.Fatalf("缺少真实清单: %+v", byID)
	}
	if byID["a-lite"] != "" {
		t.Fatalf("真实模型不应带标注: %+v", byID)
	}
}

func TestModelsContextLength(t *testing.T) {
	ts, _, _ := newRoutedProxy(t, func(c *config.Config) {
		c.Providers[0].Models = []string{"a-lite"}
		c.Providers[0].ModelContext = map[string]int{"a-model": 96000, "a-lite": 8000}
		c.Providers[1].ModelContext = map[string]int{"b-pro": 128000}
		c.Routing.VirtualModels = map[string]config.RouteTarget{
			"cops-pro":   {Provider: "b", Model: "b-pro", ContextWindow: 200000}, // 覆盖值优先
			"cops-flash": {Provider: "b", Model: "b-pro"},                        // 回退真实值 128000
			"cops-ghost": {Provider: "b", Model: "b-none"},                       // 未知 → 不带字段
		}
	})
	resp, err := http.Get(ts.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID     string `json:"id"`
			CtxLen *int   `json:"context_length"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	byID := map[string]*int{}
	for _, d := range out.Data {
		byID[d.ID] = d.CtxLen
	}
	want := map[string]int{
		"cops-pro":    200000,
		"cops-flash":  128000,
		"a-model":     96000,
		"a-lite":      8000,
		"cops-active": 96000, // 钉住名回退激活供应商默认模型
	}
	for id, n := range want {
		got := byID[id]
		if got == nil || *got != n {
			t.Errorf("%s context_length = %v, want %d", id, got, n)
		}
	}
	if byID["cops-ghost"] != nil {
		t.Errorf("未知真实模型不应带 context_length: %v", *byID["cops-ghost"])
	}
}

func TestPassthroughRouteRule(t *testing.T) {
	ts, recA, _ := newRoutedProxy(t, func(c *config.Config) {
		c.Providers[0].PassthroughModel = true
	})
	postChat(t, ts, `{"model":"client-raw-name","messages":[]}`)
	if model, _, _, _ := recA.snapshot(); model != "client-raw-name" {
		t.Fatalf("passthrough 应透传原始模型名, got %q", model)
	}
	// passthrough 时 /v1/models 不含钉住名。
	resp, err := http.Get(ts.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(b), "cops-active") {
		t.Fatalf("passthrough 下不应合成钉住名: %s", b)
	}
}

func TestResolveTargetUnit(t *testing.T) {
	cfg := config.Default()
	cfg.Active = "a"
	cfg.Providers = []config.Provider{
		{Name: "a", BaseURL: "https://a.example.com/v1", Model: "a-model"},
		{Name: "b", BaseURL: "https://b.example.com/v1"},
	}
	cfg.Routing.VirtualModels = map[string]config.RouteTarget{
		"cops-pro": {Provider: "b", Model: "b-pro"},
	}
	cfg.Routing.Utility = &config.RouteTarget{Provider: "a", Model: "a-mini"}
	cfg.Routing.DefaultVirtual = "cops-pro"

	cases := []struct {
		reqModel string
		rule     string
		provider string
		model    string
	}{
		{"gpt-5.4-nano", "utility", "a", "a-mini"},
		{"cops-pro", "virtual", "b", "b-pro"},
		{"cops-active", "pinned", "b", "b-pro"},
		{"unknown-id", "default", "a", "a-model"},
		{"", "default", "a", "a-model"},
	}
	for _, tc := range cases {
		rt := resolveTarget(cfg, tc.reqModel)
		if rt.provider == nil || rt.provider.Name != tc.provider || rt.model != tc.model || rt.rule != tc.rule {
			t.Errorf("resolveTarget(%q) = {%s %s %s}, want {%s %s %s}",
				tc.reqModel, rt.provider.Name, rt.model, rt.rule, tc.provider, tc.model, tc.rule)
		}
	}

	// 无 active 且未命中任何路由 → 空 route。
	cfg.Active = ""
	if rt := resolveTarget(cfg, "unknown"); rt.provider != nil {
		t.Error("no active provider should yield empty route")
	}
	// 无 active 但命中虚拟表 → 仍可路由。
	if rt := resolveTarget(cfg, "cops-pro"); rt.provider == nil || rt.provider.Name != "b" {
		t.Error("virtual hit should work without active provider")
	}
}
