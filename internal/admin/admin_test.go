package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cops/internal/config"
	"cops/internal/reqlog"
	"cops/internal/stats"
)

func newTestAdmin(t *testing.T, providers ...config.Provider) (*Server, *atomic.Pointer[config.Config]) {
	t.Helper()
	// 隔离 HOME，避免 Save() 覆盖真实 ~/.cops/config.json。
	t.Setenv("USERPROFILE", t.TempDir()); t.Setenv("HOME", t.TempDir())
	c := config.Default()
	c.Providers = providers
	if len(providers) > 0 {
		c.Active = providers[0].Name
	}
	var ptr atomic.Pointer[config.Config]
	ptr.Store(c)
	lg, _ := reqlog.Open(t.TempDir(), reqlog.Options{Enabled: false})
	t.Cleanup(func() { lg.Close() })
	return New(&ptr, stats.New(filepath.Join(t.TempDir(), "stats.json")), lg), &ptr
}

func TestSwitchMethod(t *testing.T) {
	s, ptr := newTestAdmin(t,
		config.Provider{Name: "a", BaseURL: "https://a.example.com"},
		config.Provider{Name: "b", BaseURL: "https://b.example.com", Model: "b-model"},
	)
	p, err := s.Switch("b", "b-new")
	if err != nil {
		t.Fatal(err)
	}
	if p.Model != "b-new" {
		t.Fatalf("model = %s", p.Model)
	}
	if got := ptr.Load().Active; got != "b" {
		t.Fatalf("active = %s", got)
	}
	if _, err := s.Switch("missing", ""); err == nil {
		t.Fatal("missing provider should error")
	}
}

func TestShutdownEndpoint(t *testing.T) {
	s, _ := newTestAdmin(t)
	var called atomic.Bool
	s.OnShutdown = func() { called.Store(true) }

	mux := http.NewServeMux()
	s.Register(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/_cops/api/shutdown", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		OK bool `json:"ok"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if !out.OK {
		t.Fatal("ok != true")
	}
	// OnShutdown 延迟异步触发
	deadline := time.Now().Add(2 * time.Second)
	for !called.Load() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !called.Load() {
		t.Fatal("OnShutdown 未被调用")
	}
}

func TestStatusIncludesInjectedAndAutostart(t *testing.T) {
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
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out["injected"]; !ok {
		t.Fatal("status 缺少 injected 字段")
	}
	if _, ok := out["autostart"]; !ok {
		t.Fatal("status 缺少 autostart 字段")
	}
}
