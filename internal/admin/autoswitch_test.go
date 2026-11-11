package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"cops/internal/config"
)

// fakeQuotaUpstream 模拟 GLM 用量端点：按供应商名返回不同的 5h 窗口占比。
func fakeQuotaUpstream(pctByProvider map[string]float64) (*httptest.Server, *atomic.Int64) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/monitor/usage/quota/limit" {
			http.NotFound(w, r)
			return
		}
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		pct, ok := pctByProvider[key]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"msg":"unknown key"}`))
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"code":200,"success":true,"data":{"limits":[
		  {"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":10000,"currentValue":%.0f,
		   "remaining":1,"percentage":%.0f,"nextResetTime":1792674331981}]}}`, pct*100, pct)
	}))
	return srv, &calls
}

// glmProviderWithFake 构造指向假用量端点的 GLM 型供应商。
func glmProviderWithFake(name, key, base string) config.Provider {
	return config.Provider{Name: name, BaseURL: base + "/api/coding/paas/v4", APIKey: key,
		Quota: &config.QuotaConfig{Kind: "glm", BaseURL: base}}
}

func TestQuotaEndpoint(t *testing.T) {
	up, _ := fakeQuotaUpstream(map[string]float64{"k1": 42.5, "k2": 3})
	defer up.Close()
	s, _ := newTestAdmin(t,
		glmProviderWithFake("glm1", "k1", up.URL),
		glmProviderWithFake("glm2", "k2", up.URL),
		config.Provider{Name: "plain", BaseURL: "https://api.deepseek.com/v1"}, // 不支持
	)
	mux := http.NewServeMux()
	s.Register(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/_cops/api/quota")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Items []struct {
			Provider string `json:"provider"`
			Usage    *struct {
				Windows []struct {
					Type  string  `json:"type"`
					Used  float64 `json:"used"`
					Limit float64 `json:"limit"`
				} `json:"windows"`
			} `json:"usage"`
			Error string `json:"error"`
		} `json:"items"`
		Active string `json:"active"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 2 {
		t.Fatalf("仅应返回支持查询的供应商，got %d", len(out.Items))
	}
	if out.Items[0].Provider != "glm1" || out.Items[0].Usage == nil {
		t.Fatalf("glm1 解析错误: %+v", out.Items[0])
	}
	w := out.Items[0].Usage.Windows[0]
	if w.Type != "five_hour" || w.Used != 4250 || w.Limit != 10000 {
		t.Errorf("glm1 窗口错误: %+v", w)
	}
	if out.Active != "glm1" {
		t.Errorf("active = %s", out.Active)
	}

	// 单供应商过滤
	resp2, err := http.Get(ts.URL + "/_cops/api/quota?name=glm2")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var out2 struct {
		Items []struct {
			Provider string `json:"provider"`
		} `json:"items"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&out2)
	if len(out2.Items) != 1 || out2.Items[0].Provider != "glm2" {
		t.Fatalf("name 过滤失败: %+v", out2.Items)
	}
}

// TestQuotaEndpointDuplicateAndScope 相同用量检测（duplicateOf）与团队范围标记：
// 两个供应商用同一账号 Key 查询（团队供应商漏配 org/project 的典型错误），
// 后者应标记 duplicateOf；正确配置团队标识的供应商 scope=team。
func TestQuotaEndpointDuplicateAndScope(t *testing.T) {
	up, _ := fakeQuotaUpstream(map[string]float64{"k1": 88})
	defer up.Close()
	team := glmProviderWithFake("glm-team", "k1", up.URL)
	team.Quota.OrganizationID, team.Quota.ProjectID = "org1", "proj1"
	s, _ := newTestAdmin(t,
		glmProviderWithFake("glm-mine", "k1", up.URL), // 个人 key
		team, // 团队供应商但漏配 → 与 glm-mine 完全相同
	)
	s.cfgPtr.Load().Providers[1].Quota.OrganizationID = "" // 模拟漏配
	s.cfgPtr.Load().Providers[1].Quota.ProjectID = ""

	mux := http.NewServeMux()
	s.Register(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/_cops/api/quota")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Items []struct {
			Provider    string `json:"provider"`
			Scope       string `json:"scope"`
			DuplicateOf string `json:"duplicateOf"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(out.Items))
	}
	if out.Items[0].Provider != "glm-mine" || out.Items[0].Scope != "personal" || out.Items[0].DuplicateOf != "" {
		t.Errorf("glm-mine 条目错误: %+v", out.Items[0])
	}
	if out.Items[1].Provider != "glm-team" || out.Items[1].DuplicateOf != "glm-mine" {
		t.Errorf("glm-team 应标记 duplicateOf=glm-mine: %+v", out.Items[1])
	}
}

func TestAutoSwitchEndpointCRUD(t *testing.T) {
	s, _ := newTestAdmin(t, config.Provider{Name: "a", BaseURL: "https://a.example.com"})
	mux := http.NewServeMux()
	s.Register(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// PUT：启用 + 阈值 90 + 候选顺序
	body := `{"autoSwitch":{"enabled":true,"thresholdPct":90,"intervalSec":120,"order":["a"]}}`
	resp, err := http.Post(ts.URL+"/_cops/api/autoswitch", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	// GET 回读
	resp2, err := http.Get(ts.URL + "/_cops/api/autoswitch")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var out struct {
		AutoSwitch config.AutoSwitchConfig `json:"autoSwitch"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.AutoSwitch.Enabled || out.AutoSwitch.ThresholdPct != 90 || out.AutoSwitch.IntervalSec != 120 {
		t.Fatalf("回读不符: %+v", out.AutoSwitch)
	}
	if len(out.AutoSwitch.Order) != 1 || out.AutoSwitch.Order[0] != "a" {
		t.Fatalf("order 不符: %v", out.AutoSwitch.Order)
	}
	// 持久化检查
	cfg, err := config.Load()
	if err != nil || !cfg.AutoSwitch.Enabled {
		t.Fatalf("配置未落盘: %+v %v", cfg.AutoSwitch, err)
	}

	// 非法阈值被拒
	bad := `{"autoSwitch":{"enabled":true,"thresholdPct":150}}`
	resp3, err := http.Post(ts.URL+"/_cops/api/autoswitch", "application/json", strings.NewReader(bad))
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusBadRequest {
		t.Fatalf("阈值 150 应被拒绝，got %d", resp3.StatusCode)
	}
	// 候选引用不存在的供应商被拒
	bad2 := `{"autoSwitch":{"order":["ghost"]}}`
	resp4, err := http.Post(ts.URL+"/_cops/api/autoswitch", "application/json", strings.NewReader(bad2))
	if err != nil {
		t.Fatal(err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusBadRequest {
		t.Fatalf("悬空候选应被拒绝，got %d", resp4.StatusCode)
	}
}

// TestAutoSwitcherTick 引擎单轮：active 超阈值 → 切到候选；事件记录；状态上报。
func TestAutoSwitcherTick(t *testing.T) {
	up, calls := fakeQuotaUpstream(map[string]float64{"k1": 96, "k2": 10})
	defer up.Close()
	s, ptr := newTestAdmin(t,
		glmProviderWithFake("glm1", "k1", up.URL), // active：96%
		glmProviderWithFake("glm2", "k2", up.URL), // 候选：10%
	)
	cfg := ptr.Load()
	cfg.AutoSwitch = config.AutoSwitchConfig{Enabled: true, ThresholdPct: 95}
	ptr.Store(cfg)

	s.switcher.tick()

	if got := ptr.Load().Active; got != "glm2" {
		t.Fatalf("应切换到 glm2，got %s", got)
	}
	evs := s.switcher.Events()
	if len(evs) != 1 || evs[0].Kind != "switch" || evs[0].From != "glm1" || evs[0].To != "glm2" {
		t.Fatalf("事件记录错误: %+v", evs)
	}
	if !strings.Contains(evs[0].Reason, "96.0%") || !strings.Contains(evs[0].Reason, "95%") {
		t.Errorf("事件原因应含用量与阈值: %q", evs[0].Reason)
	}
	lc := s.switcher.LastCheck()
	if lc == nil || lc.Active != "glm1" || len(lc.Checks) != 2 {
		t.Fatalf("lastCheck 错误: %+v", lc)
	}
	// 重复 tick：glm2（10%）不再触发，事件不重复。
	s.switcher.tick()
	if evs := s.switcher.Events(); len(evs) != 1 {
		t.Fatalf("未触发时不应新增事件: %+v", evs)
	}
	if calls.Load() < 3 {
		t.Errorf("每轮 tick 应重新查询用量（calls=%d）", calls.Load())
	}
}

// TestAutoSwitcherTickAllBlocked 全部候选超阈值：保持不变 + blocked 事件（去重）。
func TestAutoSwitcherTickAllBlocked(t *testing.T) {
	up, _ := fakeQuotaUpstream(map[string]float64{"k1": 96, "k2": 99})
	defer up.Close()
	s, ptr := newTestAdmin(t,
		glmProviderWithFake("glm1", "k1", up.URL),
		glmProviderWithFake("glm2", "k2", up.URL),
	)
	cfg := ptr.Load()
	cfg.AutoSwitch = config.AutoSwitchConfig{Enabled: true, ThresholdPct: 95}
	ptr.Store(cfg)

	s.switcher.tick()
	if got := ptr.Load().Active; got != "glm1" {
		t.Fatalf("无可用候选应保持 glm1，got %s", got)
	}
	evs := s.switcher.Events()
	if len(evs) != 1 || evs[0].Kind != "blocked" {
		t.Fatalf("应记录 blocked 事件: %+v", evs)
	}
	s.switcher.tick() // 相同结论：去重不新增
	if evs := s.switcher.Events(); len(evs) != 1 {
		t.Fatalf("blocked 事件应去重: %+v", evs)
	}
}

// TestAutoSwitcherTickDisabled 停用时 tick 无副作用。
func TestAutoSwitcherTickDisabled(t *testing.T) {
	up, calls := fakeQuotaUpstream(map[string]float64{"k1": 99})
	defer up.Close()
	s, ptr := newTestAdmin(t, glmProviderWithFake("glm1", "k1", up.URL))
	cfg := ptr.Load()
	cfg.AutoSwitch = config.AutoSwitchConfig{Enabled: false}
	ptr.Store(cfg)

	s.switcher.tick()
	if calls.Load() != 0 {
		t.Error("停用时不应查询上游")
	}
	if evs := s.switcher.Events(); len(evs) != 0 {
		t.Error("停用时不应产生事件")
	}
}

// TestStatusIncludesAutoSwitch status 端点包含自动切换摘要。
func TestStatusIncludesAutoSwitch(t *testing.T) {
	s, _ := newTestAdmin(t, config.Provider{Name: "a", BaseURL: "https://a.example.com"})
	mux := http.NewServeMux()
	s.Register(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/_cops/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		AutoSwitch struct {
			Enabled      bool    `json:"enabled"`
			ThresholdPct float64 `json:"thresholdPct"`
			IntervalSec  int     `json:"intervalSec"`
		} `json:"autoSwitch"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.AutoSwitch.Enabled {
		t.Error("默认应停用")
	}
	if out.AutoSwitch.ThresholdPct != 95 || out.AutoSwitch.IntervalSec != 300 {
		t.Errorf("默认阈值/间隔错误: %+v", out.AutoSwitch)
	}
}

// TestProvidersSanitizeQuotaKey 供应商 API 回读时用量查询密钥脱敏、保存时保留原值。
func TestProvidersSanitizeQuotaKey(t *testing.T) {
	s, _ := newTestAdmin(t)
	p := config.Provider{Name: "glm", BaseURL: "https://open.bigmodel.cn/api/coding/paas/v4",
		APIKey: "sk-chat-key", Quota: &config.QuotaConfig{Kind: "glm", APIKey: "sk-quota-key"}}
	mux := http.NewServeMux()
	s.Register(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	body, _ := json.Marshal(p)
	resp, err := http.Post(ts.URL+"/_cops/api/providers", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// GET：两个密钥都应脱敏
	resp2, err := http.Get(ts.URL + "/_cops/api/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var out struct {
		Providers []config.Provider `json:"providers"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&out)
	if len(out.Providers) != 1 {
		t.Fatalf("providers = %d", len(out.Providers))
	}
	if got := out.Providers[0].Quota.APIKey; got == "sk-quota-key" {
		t.Fatalf("quota.apiKey 未脱敏: %q", got)
	}
	if got := out.Providers[0].APIKey; got == "sk-chat-key" {
		t.Fatalf("apiKey 未脱敏: %q", got)
	}

	// 前端语义：编辑回传不带 quota key（空值）→ 原值保留
	p2 := out.Providers[0]
	p2.Model = "glm-4.7"
	p2.Quota.APIKey = ""
	body2, _ := json.Marshal(p2)
	resp3, err := http.Post(ts.URL+"/_cops/api/providers", "application/json", strings.NewReader(string(body2)))
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()

	cfg := s.cfgPtr.Load()
	got, _ := cfg.Find("glm")
	if got.Quota == nil || got.Quota.APIKey != "sk-quota-key" {
		t.Fatalf("空 quota key 回传应保留原值: %+v", got.Quota)
	}
	if got.Model != "glm-4.7" {
		t.Fatalf("model 更新失败: %s", got.Model)
	}

	// CLI 语义：GET 回读的脱敏 key（形如 "sk-c...-key"）原样回传 → 同样保留原值。
	// 注意 Find 返回配置内部指针，必须复制值再改，避免污染共享状态。
	p3 := *got
	q3 := *got.Quota
	p3.Quota = &q3
	p3.APIKey = config.MaskKey(got.APIKey)
	p3.Quota.APIKey = config.MaskKey(got.Quota.APIKey)
	body3, _ := json.Marshal(p3)
	resp4, err := http.Post(ts.URL+"/_cops/api/providers", "application/json", strings.NewReader(string(body3)))
	if err != nil {
		t.Fatal(err)
	}
	resp4.Body.Close()

	cfg = s.cfgPtr.Load()
	got, _ = cfg.Find("glm")
	if got.APIKey != "sk-chat-key" {
		t.Fatalf("脱敏 apiKey 回传应保留原值: %q", got.APIKey)
	}
	if got.Quota == nil || got.Quota.APIKey != "sk-quota-key" {
		t.Fatalf("脱敏 quota key 回传应保留原值: %+v", got.Quota)
	}
}

// TestProvidersQuotaKeyNoFallbackPollution 旧 quota 未配 apiKey 时，更新供应商
// 不得把主 API Key 回退灌入 quota.apiKey（配置污染）。
func TestProvidersQuotaKeyNoFallbackPollution(t *testing.T) {
	s, _ := newTestAdmin(t)
	s.cfgPtr.Load().Providers = append(s.cfgPtr.Load().Providers, config.Provider{
		Name: "glm", BaseURL: "https://open.bigmodel.cn/api/coding/paas/v4",
		APIKey: "sk-chat-key", Quota: &config.QuotaConfig{Kind: "glm"},
	})
	mux := http.NewServeMux()
	s.Register(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// 模拟 cops quota team：GET（脱敏）→ 绑定 org/project（key 留空）→ POST。
	resp, err := http.Get(ts.URL + "/_cops/api/providers")
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Providers []config.Provider `json:"providers"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	p := list.Providers[0]
	p.Quota.OrganizationID, p.Quota.ProjectID = "org1", "proj1"
	body, _ := json.Marshal(p)
	resp2, err := http.Post(ts.URL+"/_cops/api/providers", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()

	cfg := s.cfgPtr.Load()
	got, _ := cfg.Find("glm")
	if got.Quota == nil || got.Quota.APIKey != "" {
		t.Fatalf("quota.apiKey 不应被灌入主 Key: %+v", got.Quota)
	}
	if got.Quota.OrganizationID != "org1" || got.Quota.ProjectID != "proj1" {
		t.Fatalf("团队标识未保存: %+v", got.Quota)
	}
	if got.APIKey != "sk-chat-key" {
		t.Fatalf("主 Key 不应被脱敏值覆盖: %q", got.APIKey)
	}
}
