package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"cops/internal/config"
	"cops/internal/reqlog"
	"cops/internal/stats"
)

func newTestServer(t *testing.T, upstream *httptest.Server, active string) (*Server, *stats.Tracker) {
	t.Helper()
	c := config.Default()
	c.Active = active
	c.Providers = []config.Provider{{
		Name: active, BaseURL: upstream.URL, APIKey: "sk-upstream-key", Model: "real-model",
		Prices: map[string]config.ModelPrice{"real-model": {InputPerMillionTokens: 1, OutputPerMillionTokens: 2}},
	}}
	var ptr atomic.Pointer[config.Config]
	ptr.Store(c)
	st := stats.New(filepath.Join(t.TempDir(), "stats.json"))
	lg, _ := reqlog.Open(t.TempDir(), reqlog.Options{Enabled: false})
	t.Cleanup(func() { lg.Close() })
	return New(&ptr, st, lg), st
}

func TestNonStreamChatCompletion(t *testing.T) {
	var gotAuth, gotModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","choices":[{"message":{"role":"assistant","content":"你好"}}],"usage":{"prompt_tokens":11,"completion_tokens":22}}`))
	}))
	defer up.Close()

	s, st := newTestServer(t, up, "test")
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"cops-active","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)

	if gotAuth != "Bearer sk-upstream-key" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotModel != "real-model" {
		t.Fatalf("model = %q, want real-model", gotModel)
	}
	if !strings.Contains(string(b), "你好") {
		t.Fatalf("body = %s", b)
	}
	rows := st.Summary(1)
	if len(rows) != 1 || rows[0].InputTokens != 11 || rows[0].OutputTokens != 22 {
		t.Fatalf("stats = %+v", rows)
	}
	// 11/1M*1 + 22/1M*2 = 0.000055
	if rows[0].Cost < 0.0000549 || rows[0].Cost > 0.0000551 {
		t.Fatalf("cost = %v", rows[0].Cost)
	}
}

func TestStreamPassthroughAndUsage(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2}}\n\n" +
		"data: [DONE]\n\n"
	var gotStreamOptions bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		so, _ := body["stream_options"].(map[string]any)
		gotStreamOptions = so["include_usage"] == true
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer up.Close()

	s, st := newTestServer(t, up, "test")
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"cops-active","stream":true,"messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)

	if gotStreamOptions != true {
		t.Fatal("stream_options.include_usage 未注入")
	}
	if string(b) != sse {
		t.Fatalf("SSE 未原样透传:\n%s", b)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %s", ct)
	}
	rows := st.Summary(1)
	if len(rows) != 1 || rows[0].InputTokens != 7 || rows[0].OutputTokens != 2 {
		t.Fatalf("stream usage stats = %+v", rows)
	}
}

func TestNoActiveProvider(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	s, _ := newTestServer(t, up, "test")
	c := config.Default() // 无 active
	var ptr atomic.Pointer[config.Config]
	ptr.Store(c)
	s.cfgPtr = &ptr

	ts := httptest.NewServer(s)
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestModelsSynthesis(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("/v1/models 不应转发上游（有清单时）")
	}))
	defer up.Close()
	s, _ := newTestServer(t, up, "test")
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Data) == 0 || out.Data[0].ID != "cops-active" {
		t.Fatalf("models = %+v", out.Data)
	}
	found := map[string]bool{}
	for _, d := range out.Data {
		found[d.ID] = true
	}
	if !found["real-model"] {
		t.Fatalf("缺少 provider model: %+v", out.Data)
	}
}

func TestResponsesAPIUsage(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n" +
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":9,"output_tokens":4}}}` + "\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer up.Close()
	s, st := newTestServer(t, up, "test")
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/responses", "application/json",
		strings.NewReader(`{"model":"cops-active","stream":true,"input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	rows := st.Summary(1)
	if len(rows) != 1 || rows[0].InputTokens != 9 || rows[0].OutputTokens != 4 {
		t.Fatalf("responses usage = %+v", rows)
	}
}

func TestUpstreamErrorPassthrough(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","code":429}}`))
	}))
	defer up.Close()
	s, st := newTestServer(t, up, "test")
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	rows := st.Summary(1)
	if len(rows) != 1 || rows[0].Errors != 1 {
		t.Fatalf("error not counted: %+v", rows)
	}
}

func TestUpstreamUnreachable(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	up.Close() // 立即关闭 → 不可达
	s, _ := newTestServer(t, up, "test")
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var e map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&e)
	if !strings.Contains(e["error"].(map[string]any)["message"].(string), "上游") {
		t.Fatalf("error = %+v", e)
	}
}

func TestLineScannerAcrossChunks(t *testing.T) {
	ls := newLineScanner()
	var lines [][]byte
	lines = append(lines, ls.feed([]byte("data: {\"a\""))...)
	lines = append(lines, ls.feed([]byte(":1}\ndata: [DONE]\n"))...)
	if len(lines) != 2 || string(lines[0]) != `data: {"a":1}` || string(lines[1]) != "data: [DONE]" {
		t.Fatalf("lines = %q", lines)
	}
}

func TestUpstreamPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/v1/chat/completions", "/chat/completions"},
		{"/v1/models", "/models"},
		{"/v1", "/"},
		{"/chat/completions", "/chat/completions"},
		{"/v1/responses", "/responses"},
	}
	for _, c := range cases {
		if got := upstreamPath(c.in); got != c.want {
			t.Errorf("upstreamPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 回归测试：上游用显式路径注册（模拟真实 OpenAI 兼容服务），
// 确保 /v1 前缀被正确剥离、不出现 /v1/v1/...。
func TestExplicitUpstreamPaths(t *testing.T) {
	mux := http.NewServeMux()
	var hitPath string
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	})
	up := httptest.NewServer(mux)
	defer up.Close()

	// BaseURL 按约定含 /v1（OpenAI SDK 风格）
	c := config.Default()
	c.Active = "test"
	c.Providers = []config.Provider{{Name: "test", BaseURL: up.URL + "/v1", APIKey: "k", Model: "m"}}
	var ptr atomic.Pointer[config.Config]
	ptr.Store(c)
	st := stats.New(filepath.Join(t.TempDir(), "stats.json"))
	lg, _ := reqlog.Open(t.TempDir(), reqlog.Options{Enabled: false})
	defer lg.Close()
	ts := httptest.NewServer(New(&ptr, st, lg))
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"x","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if hitPath != "/v1/chat/completions" {
		t.Fatalf("upstream saw %q, want /v1/chat/completions（说明 /v1 前缀未正确处理）", hitPath)
	}
}
