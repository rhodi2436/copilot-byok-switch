package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"cops/internal/config"
)

// TestQueryGLMCreditPlan 新套餐（credit 制，data 信封）。
func TestQueryGLMCreditPlan(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"msg":"操作成功","success":true,
		  "data":{"level":"lite","limits":[
		    {"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":2000,"currentValue":1900,
		     "remaining":100,"percentage":95,"nextResetTime":1792674331981},
		    {"type":"CREDIT_LIMIT","unit":6,"number":1,"usage":10000,"currentValue":3166,
		     "remaining":6833,"percentage":31,"nextResetTime":1793279131981},
		    {"type":"TIME_LIMIT","unit":5,"number":1,"usage":1000,"currentValue":9,
		     "remaining":991,"percentage":0,"nextResetTime":1793279131981}]}}`))
	}))
	defer srv.Close()

	p := &config.Provider{Name: "glm", BaseURL: srv.URL + "/api/coding/paas/v4", APIKey: "sk-test"}
	u, err := queryGLM(context.Background(), srv.Client(), p)
	if err != nil {
		t.Fatalf("queryGLM: %v", err)
	}
	if gotAuth != "sk-test" {
		t.Errorf("Authorization 应为裸 key，got %q", gotAuth)
	}
	if gotPath != "/api/monitor/usage/quota/limit" {
		t.Errorf("请求路径错误: %q", gotPath)
	}
	if len(u.Windows) != 2 { // TIME_LIMIT 应被跳过
		t.Fatalf("窗口数 = %d, want 2（TIME_LIMIT 不计入）: %+v", len(u.Windows), u.Windows)
	}
	w5h := u.Windows[0]
	if w5h.Type != "five_hour" || w5h.Used != 1900 || w5h.Limit != 2000 || w5h.Percent() != 95 {
		t.Errorf("5h 窗口解析错误: %+v", w5h)
	}
	if w5h.ResetAt.UnixMilli() != 1792674331981 {
		t.Errorf("5h 重置时间错误: %v", w5h.ResetAt)
	}
	if w := u.Windows[1]; w.Type != "weekly" || w.Percent() != 31.66 {
		t.Errorf("周窗口解析错误: %+v (%.2f%%)", w, w.Percent())
	}
	if pc := u.BindingPercent(); pc < 94.99 || pc > 95.01 {
		t.Errorf("BindingPercent = %v, want 95", pc)
	}
}

// TestQueryGLMLegacyTopLevel 旧套餐（token 制，顶层 limits 无 data 信封、仅百分比）。
func TestQueryGLMLegacyTopLevel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"limits":[
		    {"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":"2","nextResetTime":1774967594803}],
		  "level":"pro"}`))
	}))
	defer srv.Close()

	p := &config.Provider{Name: "glm", BaseURL: srv.URL, APIKey: "k"}
	u, err := queryGLM(context.Background(), srv.Client(), p)
	if err != nil {
		t.Fatalf("queryGLM: %v", err)
	}
	if len(u.Windows) != 1 {
		t.Fatalf("窗口数 = %d, want 1: %+v", len(u.Windows), u.Windows)
	}
	w := u.Windows[0]
	if w.Type != "five_hour" {
		t.Errorf("窗口类型 = %q, want five_hour", w.Type)
	}
	// 仅百分比（字符串形态）时归一为百分制。
	if w.Used != 2 || w.Limit != 100 || w.Percent() != 2 {
		t.Errorf("百分比回退解析错误: %+v", w)
	}
}

// TestQueryGLMTeamOrg 团队版头与查询参数。
func TestQueryGLMTeamOrg(t *testing.T) {
	var gotQuery string
	var gotOrg, gotProj string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotOrg = r.Header.Get("bigmodel-organization")
		gotProj = r.Header.Get("bigmodel-project")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"success":true,"data":{"limits":[
		    {"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":28000,"currentValue":100,"percentage":0}]}}`))
	}))
	defer srv.Close()

	p := &config.Provider{Name: "glm-team", BaseURL: srv.URL, APIKey: "k",
		Quota: &config.QuotaConfig{OrganizationID: "org1", ProjectID: "proj1"}}
	if _, err := queryGLM(context.Background(), srv.Client(), p); err != nil {
		t.Fatalf("queryGLM: %v", err)
	}
	if gotQuery != "type=2" || gotOrg != "org1" || gotProj != "proj1" {
		t.Errorf("团队版参数错误: query=%q org=%q proj=%q", gotQuery, gotOrg, gotProj)
	}
}

// TestQueryGLMErrors 错误路径：无套餐 / HTTP 错误 / 空限额。
func TestQueryGLMErrors(t *testing.T) {
	mk := func(body string, status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
	}
	cases := []struct {
		name, body string
		status     int
		wantSub    string
	}{
		{"no-plan", `{"code":500,"msg":"当前用户不存在coding plan","success":false}`, 200, "不存在coding plan"},
		{"http-401", `{"error":"unauthorized"}`, 401, "HTTP 401"},
		{"empty-limits", `{"code":200,"success":true,"data":{"limits":[]}}`, 200, "不含限额数据"},
	}
	for _, c := range cases {
		srv := mk(c.body, c.status)
		p := &config.Provider{Name: "x", BaseURL: srv.URL, APIKey: "k"}
		_, err := queryGLM(context.Background(), srv.Client(), p)
		if err == nil {
			srv.Close()
			t.Fatalf("%s: 应返回错误", c.name)
		}
		if !contains(err.Error(), c.wantSub) {
			t.Errorf("%s: 错误 %q 应包含 %q", c.name, err.Error(), c.wantSub)
		}
		srv.Close()
	}
}

// TestGLMBaseURLOverride quota.baseUrl 覆盖优先于 BaseURL 推导。
func TestGLMBaseURLOverride(t *testing.T) {
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("不应请求 chat 域名")
	}))
	defer chat.Close()
	quotaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"limits":[{"type":"CREDIT_LIMIT","unit":6,"number":1,"usage":100,"currentValue":1}]}`))
	}))
	defer quotaSrv.Close()

	p := &config.Provider{Name: "x", BaseURL: chat.URL + "/v1", APIKey: "k",
		Quota: &config.QuotaConfig{BaseURL: quotaSrv.URL}}
	u, err := queryGLM(context.Background(), quotaSrv.Client(), p)
	if err != nil {
		t.Fatalf("queryGLM: %v", err)
	}
	if u.BindingPercent() != 1 {
		t.Errorf("BindingPercent = %v, want 1", u.BindingPercent())
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
