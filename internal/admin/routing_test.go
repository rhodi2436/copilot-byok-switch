package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cops/internal/config"
)

func routingTestMux(t *testing.T, providers ...config.Provider) (*Server, *httptest.Server) {
	t.Helper()
	s, _ := newTestAdmin(t, providers...)
	mux := http.NewServeMux()
	s.Register(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, ts
}

func putRouting(t *testing.T, ts *httptest.Server, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/_cops/api/routing", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestRoutingGetPut(t *testing.T) {
	_, ts := routingTestMux(t,
		config.Provider{Name: "a", BaseURL: "https://a.example.com", Model: "a-model"},
		config.Provider{Name: "b", BaseURL: "https://b.example.com", Model: "b-model"},
	)

	// 初始（零值路由）
	resp, err := http.Get(ts.URL + "/_cops/api/routing")
	if err != nil {
		t.Fatal(err)
	}
	var init struct {
		Routing config.RoutingConfig `json:"routing"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&init)
	resp.Body.Close()
	if len(init.Routing.VirtualModels) != 0 || init.Routing.Utility != nil {
		t.Fatalf("初始路由应为零值: %+v", init.Routing)
	}

	// 合法 PUT
	payload := `{"routing":{"virtualModels":{"cops-pro":{"provider":"a","model":"a-big"},"cops-flash":{"provider":"b","model":"b-fast"}},"defaultVirtual":"cops-pro","utility":{"provider":"b","model":"b-fast"}}}`
	if code, out := putRouting(t, ts, payload); code != http.StatusOK || out["ok"] != true {
		t.Fatalf("PUT = %d %+v", code, out)
	}

	// 读回
	resp, _ = http.Get(ts.URL + "/_cops/api/routing")
	var got struct {
		Routing config.RoutingConfig `json:"routing"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if got.Routing.DefaultVirtual != "cops-pro" || len(got.Routing.VirtualModels) != 2 || got.Routing.Utility == nil {
		t.Fatalf("读回不符: %+v", got.Routing)
	}

	// 非法 PUT（供应商不存在）→ 400 且不生效
	code, out := putRouting(t, ts, `{"routing":{"virtualModels":{"x":{"provider":"ghost","model":"m"}}}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("悬空引用应 400, got %d %+v", code, out)
	}
	resp, _ = http.Get(ts.URL + "/_cops/api/routing")
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if len(got.Routing.VirtualModels) != 2 {
		t.Fatalf("失败的 PUT 不应改变配置: %+v", got.Routing)
	}
}

func TestUseModel(t *testing.T) {
	s, ts := routingTestMux(t,
		config.Provider{Name: "a", BaseURL: "https://a.example.com", Model: "a-model"},
	)
	payload := `{"routing":{"virtualModels":{"cops-pro":{"provider":"a","model":"a-big"},"cops-flash":{"provider":"a","model":"a-fast"}},"defaultVirtual":"cops-pro"}}`
	if code, _ := putRouting(t, ts, payload); code != http.StatusOK {
		t.Fatal("PUT routing failed")
	}

	// 方法直调
	name, err := s.UseModel("cops-flash")
	if err != nil || name != "cops-flash" {
		t.Fatalf("UseModel = %q %v", name, err)
	}
	if s.cfgPtr.Load().Routing.DefaultVirtual != "cops-flash" {
		t.Fatal("DefaultVirtual 未更新")
	}
	if _, err := s.UseModel("ghost"); err == nil {
		t.Fatal("不存在的虚拟模型应报错")
	}

	// HTTP 端点
	resp, err := http.Post(ts.URL+"/_cops/api/routing/use", "application/json",
		bytes.NewReader([]byte(`{"name":"cops-pro"}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		OK             bool   `json:"ok"`
		DefaultVirtual string `json:"defaultVirtual"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK || !out.OK || out.DefaultVirtual != "cops-pro" {
		t.Fatalf("use = %d %+v", resp.StatusCode, out)
	}
}

func TestStatusRoutingSummary(t *testing.T) {
	_, ts := routingTestMux(t, config.Provider{Name: "a", BaseURL: "https://a.example.com"})
	payload := `{"routing":{"virtualModels":{"cops-pro":{"provider":"a","model":"a-big"},"cops-flash":{"provider":"a","model":"a-fast"}},"defaultVirtual":"cops-pro","utility":{"provider":"a","model":"a-fast"}}}`
	putRouting(t, ts, payload)

	resp, err := http.Get(ts.URL + "/_cops/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Routing struct {
			DefaultVirtual string   `json:"defaultVirtual"`
			VirtualModels  []string `json:"virtualModels"`
			Utility        struct {
				Provider string `json:"provider"`
			} `json:"utility"`
		} `json:"routing"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Routing.DefaultVirtual != "cops-pro" {
		t.Fatalf("defaultVirtual = %q", out.Routing.DefaultVirtual)
	}
	if len(out.Routing.VirtualModels) != 2 || out.Routing.VirtualModels[0] != "cops-flash" {
		t.Fatalf("virtualModels = %+v", out.Routing.VirtualModels)
	}
	if out.Routing.Utility.Provider != "a" {
		t.Fatalf("utility = %+v", out.Routing.Utility)
	}
}

func TestDeleteProviderScrubsRouting(t *testing.T) {
	_, ts := routingTestMux(t,
		config.Provider{Name: "a", BaseURL: "https://a.example.com"},
		config.Provider{Name: "b", BaseURL: "https://b.example.com"},
	)
	payload := `{"routing":{"virtualModels":{"cops-pro":{"provider":"a","model":"a-big"},"cops-b":{"provider":"b","model":"b-1"}},"defaultVirtual":"cops-pro","utility":{"provider":"b","model":"b-2"}}}`
	putRouting(t, ts, payload)

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/_cops/api/providers?name=a", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	r2, _ := http.Get(ts.URL + "/_cops/api/routing")
	var got struct {
		Routing config.RoutingConfig `json:"routing"`
	}
	_ = json.NewDecoder(r2.Body).Decode(&got)
	r2.Body.Close()
	if _, ok := got.Routing.VirtualModels["cops-pro"]; ok {
		t.Fatal("删除供应商后应移除其虚拟模型引用")
	}
	if _, ok := got.Routing.VirtualModels["cops-b"]; !ok {
		t.Fatal("无关引用不应被误删")
	}
	if got.Routing.DefaultVirtual != "" {
		t.Fatalf("悬空 DefaultVirtual 应清空, got %q", got.Routing.DefaultVirtual)
	}
	if got.Routing.Utility == nil || got.Routing.Utility.Provider != "b" {
		t.Fatal("utility 指向 b 不应被清除")
	}
}
