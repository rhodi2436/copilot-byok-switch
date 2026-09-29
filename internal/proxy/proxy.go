// Package proxy 实现本地 OpenAI 兼容反向代理：
// 将 /v1/* 请求转发到当前激活供应商，注入真实 API Key、
// 改写 model 字段、透传 SSE 流并捕获用量。
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"cops/internal/config"
	"cops/internal/reqlog"
	"cops/internal/stats"
)

// hardBodyCap 超过此大小（64MB）的请求体不再做改写，直接透传。
const hardBodyCap = 64 << 20

// Server 核心代理。cfgPtr 与 admin 模块共享，切换供应商即时生效。
type Server struct {
	cfgPtr *atomic.Pointer[config.Config]
	stats  *stats.Tracker
	log    *reqlog.Logger
	client *http.Client
}

// New 创建代理服务。
func New(cfgPtr *atomic.Pointer[config.Config], st *stats.Tracker, lg *reqlog.Logger) *Server {
	return &Server{
		cfgPtr: cfgPtr,
		stats:  st,
		log:    lg,
		client: &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   15 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 16,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// route 单次请求的路由解析结果。
type route struct {
	provider *config.Provider // 转发目标供应商（BaseURL/密钥/超时/ExtraHeaders 等取自它）
	model    string           // 改写后的真实模型名；空 = 不改写（透传）
	rule     string           // 命中规则：utility/virtual/pinned/default/passthrough
}

// resolveTarget 按请求体 model 名做确定性四级路由（ROADMAP v0.2）：
//
//	1. utility — 命中 UtilityPatterns（Copilot CLI 会把 gpt-5.4-nano 等内部
//	   id 直发 BYOK 端点用于 compaction/auto-mode，见 copilot-cli#4950）
//	2. virtual — 虚拟模型表命中，可跨供应商
//	3. pinned  — 钉住名（COPILOT_MODEL 指向的 cfg.VirtualModel）→ DefaultVirtual 表项
//	4. default — 激活供应商兜底；未知 id 也走这里，永不向上游 404
//
// 命中 1-3 时转发参数取目标供应商；PassthroughModel 仅在 default 兜底时生效。
func resolveTarget(cfg *config.Config, reqModel string) route {
	r := &cfg.Routing
	if r.MatchesUtility(reqModel) {
		if p, i := cfg.Find(r.Utility.Provider); i >= 0 {
			return route{provider: p, model: r.Utility.Model, rule: "utility"}
		}
	}
	if reqModel != "" {
		if t, ok := r.VirtualModels[reqModel]; ok {
			if p, i := cfg.Find(t.Provider); i >= 0 {
				return route{provider: p, model: t.Model, rule: "virtual"}
			}
		}
	}
	if reqModel != "" && reqModel == cfg.VirtualModel && r.DefaultVirtual != "" {
		if t, ok := r.VirtualModels[r.DefaultVirtual]; ok {
			if p, i := cfg.Find(t.Provider); i >= 0 {
				return route{provider: p, model: t.Model, rule: "pinned"}
			}
		}
	}
	p := cfg.ActiveProvider()
	if p == nil {
		return route{}
	}
	if p.PassthroughModel {
		return route{provider: p, rule: "passthrough"}
	}
	return route{provider: p, model: p.Model, rule: "default"}
}

type usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
}

// scanTarget 兼容 Chat Completions（顶层 usage）与 Responses API（response.usage）两种位置。
type scanTarget struct {
	Usage    *usage `json:"usage"`
	Response *struct {
		Usage *usage `json:"usage"`
	} `json:"response"`
}

func (u *usage) totalIn() int64 {
	if u.PromptTokens > 0 {
		return u.PromptTokens
	}
	return u.InputTokens
}

func (u *usage) totalOut() int64 {
	if u.CompletionTokens > 0 {
		return u.CompletionTokens
	}
	return u.OutputTokens
}

func writeJSONError(w http.ResponseWriter, status int, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": fmt.Sprintf(format, args...),
			"type":    "cops_proxy_error",
		},
	})
}

// hop-by-hop 与由 transport 管理的头，不透传。
var skipReqHeaders = map[string]bool{
	"Authorization":  true,
	"Host":           true,
	"Content-Length": true,
	"Connection":     true,
	"Keep-Alive":     true,
	"Transfer-Encoding": true,
	"Accept-Encoding":   true,
}

var skipRespHeaders = map[string]bool{
	"Content-Length":    true,
	"Connection":        true,
	"Keep-Alive":        true,
	"Transfer-Encoding": true,
}

// ServeHTTP 处理 /v1/* 请求。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	cfg := s.cfgPtr.Load()

	entry := reqlog.Entry{
		Method: r.Method,
		Path:   r.URL.Path,
	}

	// GET /v1/models：本地合成（钉住名 + 虚拟名 + 真实清单），避免依赖上游可用性。
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
		s.handleModels(w, r, cfg)
		return
	}

	// 读取请求体（带上限），JSON 时改写 model / 注入 stream_options。
	var upstreamBody io.Reader
	body, err := io.ReadAll(io.LimitReader(r.Body, hardBodyCap+1))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "cops: 读取请求体失败: %v", err)
		return
	}
	// 先探测请求体中的 model 名做路由解析（超大体的前 64MB 足够包含 model 字段）。
	var probe struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &probe)

	rt := resolveTarget(cfg, probe.Model)
	if rt.provider == nil {
		entry.Error = "no route / no active provider"
		entry.Time = start.Format("2006-01-02 15:04:05.000")
		s.log.Log(entry)
		writeJSONError(w, http.StatusServiceUnavailable,
			"cops: 尚未配置或激活任何供应商，且该请求未命中任何路由。请运行 cops add 添加，再运行 cops switch <名称> 激活。")
		return
	}
	p := rt.provider
	entry.Provider = p.Name
	entry.RouteRule = rt.rule
	if rt.model != "" && probe.Model != "" && probe.Model != rt.model {
		entry.VirtualModel = probe.Model
	}

	if int64(len(body)) > hardBodyCap {
		// 超大请求体：不做改写，直接透传剩余部分。
		upstreamBody = io.MultiReader(bytes.NewReader(body), r.Body)
	} else {
		rewritten := s.rewriteBody(body, p, rt.model, &entry)
		upstreamBody = bytes.NewReader(rewritten)
	}

	// Copilot CLI 请求 /v1/xxx；BaseURL 按约定已含 /v1（OpenAI SDK 风格），
	// 因此剥离请求路径的 /v1 前缀再拼接，避免 /v1/v1/...。
	upstreamURL := p.BaseURL + upstreamPath(r.URL.Path)
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(p.TimeoutOrDefault())*time.Second)
	defer cancel()
	upReq, err := http.NewRequestWithContext(ctx, r.Method, upstreamURL, upstreamBody)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "cops: 构造上游请求失败: %v", err)
		return
	}
	for k, vv := range r.Header {
		if skipReqHeaders[k] {
			continue
		}
		upReq.Header[k] = vv
	}
	if p.APIKey != "" {
		upReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	for k, v := range p.ExtraHeaders {
		upReq.Header.Set(k, v)
	}
	if upReq.Header.Get("Content-Type") == "" {
		upReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.client.Do(upReq)
	if err != nil {
		dur := time.Since(start).Milliseconds()
		entry.Status = http.StatusBadGateway
		entry.DurationMs = dur
		entry.Error = fmt.Sprintf("上游请求失败: %v", err)
		entry.Time = start.Format("2006-01-02 15:04:05.000")
		if len(body) <= hardBodyCap {
			entry.RequestBody = string(body)
		}
		s.log.Log(entry)
		s.stats.Record(p.Name, entry.Model, 0, 0, 0, true)
		writeJSONError(w, http.StatusBadGateway, "cops: 上游 %s 请求失败: %v", p.Name, err)
		return
	}
	defer resp.Body.Close()

	entry.Status = resp.StatusCode
	for k, vv := range resp.Header {
		if skipRespHeaders[k] {
			continue
		}
		w.Header()[k] = vv
	}

	ct := resp.Header.Get("Content-Type")
	isSSE := strings.HasPrefix(ct, "text/event-stream")
	entry.Stream = isSSE

	if isSSE {
		s.streamResponse(w, r, resp.Body, p, &entry, start)
		return
	}

	// 非流式：完整读取（限 16MB）、解析 usage、原样回写。
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "cops: 读取上游响应失败: %v", err)
		return
	}
	var target scanTarget
	if resp.StatusCode < 300 && json.Unmarshal(respBody, &target) == nil && target.Usage != nil {
		u := target.Usage
		entry.PromptTokens = u.totalIn()
		entry.CompletionTokens = u.totalOut()
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)

	entry.DurationMs = time.Since(start).Milliseconds()
	entry.Time = start.Format("2006-01-02 15:04:05.000")
	entry.ResponseBody = string(respBody)
	s.finish(p, &entry)
}

// upstreamPath 剥离请求路径的 /v1 前缀（BaseURL 已按 OpenAI SDK 约定包含 /v1）。
// 非 /v1 开头的路径原样保留，兼容其他客户端写法。
func upstreamPath(p string) string {
	if strings.HasPrefix(p, "/v1/") {
		return p[3:]
	}
	if p == "/v1" {
		return "/"
	}
	return p
}

// rewriteBody 改写 JSON 请求体：model 重映射 + 流式请求注入 include_usage +
// 按供应商剥离采样参数。targetModel 为空表示不改写 model。
func (s *Server) rewriteBody(body []byte, p *config.Provider, targetModel string, entry *reqlog.Entry) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	changed := false
	if targetModel != "" {
		if cur, ok := m["model"].(string); !ok || cur != targetModel {
			m["model"] = targetModel
			changed = true
		}
	}
	// Copilot CLI 强制发送 temperature:0 / top_p:0.95（copilot-cli#4950），
	// 部分上游不接受这些字段；StripSampling 按供应商剥离。
	if p.StripSampling {
		for _, k := range []string{"temperature", "top_p", "frequency_penalty", "presence_penalty"} {
			if _, ok := m[k]; ok {
				delete(m, k)
				changed = true
			}
		}
	}
	if stream, _ := m["stream"].(bool); stream && !p.NoUsageInjection {
		if _, exists := m["stream_options"]; !exists {
			m["stream_options"] = map[string]any{"include_usage": true}
			changed = true
		}
	}
	entry.RequestBody = string(body)
	if mdl, _ := m["model"].(string); mdl != "" {
		entry.Model = mdl
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	entry.RequestBody = string(out)
	return out
}

// handleModels 合成 /v1/models 响应：钉住名 + 虚拟模型名（附 cops_target 标注）+
// 激活供应商真实清单；全部为空时透传上游。
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request, cfg *config.Config) {
	p := cfg.ActiveProvider()
	type item struct {
		id, target string
		ctxWin     int
	}
	var list []item
	seen := map[string]bool{}
	add := func(id, target string, ctxWin int) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		list = append(list, item{id, target, ctxWin})
	}

	// 钉住名（COPILOT_MODEL 指向它），标注其默认去向与生效上下文
	// （DefaultVirtual 表项 > 激活供应商默认模型；passthrough 无已知值）。
	if cfg.VirtualModel != "" && (p == nil || !p.PassthroughModel) {
		pinCtx := 0
		if t, ok := cfg.Routing.VirtualModels[cfg.Routing.DefaultVirtual]; ok && cfg.Routing.DefaultVirtual != "" {
			pinCtx = cfg.TargetContext(t)
		} else if p != nil {
			pinCtx = p.ModelContext[p.Model]
		}
		add(cfg.VirtualModel, defaultTargetAnnotation(cfg, p), pinCtx)
	}
	for _, name := range cfg.Routing.SortedVirtualModels() {
		t := cfg.Routing.VirtualModels[name]
		add(name, t.Provider+"/"+t.Model, cfg.TargetContext(t))
	}
	if p != nil {
		add(p.Model, "", p.ModelContext[p.Model])
		for _, m := range p.Models {
			add(m, "", p.ModelContext[m])
		}
	}

	if len(list) == 0 {
		if p == nil {
			writeJSONError(w, http.StatusServiceUnavailable,
				"cops: 尚未配置或激活任何供应商。请运行 cops add 添加，再运行 cops switch <名称> 激活。")
			return
		}
		s.forwardRaw(w, r, p, nil)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	owner := "cops"
	if p != nil {
		owner = p.Name
	}
	data := make([]any, 0, len(list))
	for _, it := range list {
		m := map[string]any{
			"id": it.id, "object": "model",
			"created": time.Now().Unix(), "owned_by": owner,
		}
		if it.target != "" {
			m["cops_target"] = it.target
		}
		if it.ctxWin > 0 {
			m["context_length"] = it.ctxWin
		}
		data = append(data, m)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

// defaultTargetAnnotation 钉住名条目的 cops_target 标注：
// DefaultVirtual 表项 > passthrough > 激活供应商默认模型。
func defaultTargetAnnotation(cfg *config.Config, p *config.Provider) string {
	if cfg.Routing.DefaultVirtual != "" {
		if t, ok := cfg.Routing.VirtualModels[cfg.Routing.DefaultVirtual]; ok {
			return t.Provider + "/" + t.Model
		}
	}
	if p == nil {
		return ""
	}
	if p.PassthroughModel {
		return "passthrough"
	}
	if p.Model != "" {
		return p.Name + "/" + p.Model
	}
	return p.Name
}

// forwardRaw 原样转发（用于无清单时的 /v1/models 等）。
func (s *Server) forwardRaw(w http.ResponseWriter, r *http.Request, p *config.Provider, body []byte) {
	upstreamURL := p.BaseURL + upstreamPath(r.URL.Path)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(p.TimeoutOrDefault())*time.Second)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	} else if r.Body != nil {
		rd = r.Body
	}
	upReq, err := http.NewRequestWithContext(ctx, r.Method, upstreamURL, rd)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "cops: %v", err)
		return
	}
	for k, vv := range r.Header {
		if skipReqHeaders[k] {
			continue
		}
		upReq.Header[k] = vv
	}
	if p.APIKey != "" {
		upReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	for k, v := range p.ExtraHeaders {
		upReq.Header.Set(k, v)
	}
	resp, err := s.client.Do(upReq)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "cops: 上游 %s 请求失败: %v", p.Name, err)
		return
	}
	defer resp.Body.Close()
	for k, vv := range resp.Header {
		if skipRespHeaders[k] {
			continue
		}
		w.Header()[k] = vv
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// streamResponse 透传 SSE 流：逐块回写并即时刷新；同时扫描 data: 行捕获最终 usage。
func (s *Server) streamResponse(w http.ResponseWriter, r *http.Request, body io.Reader, p *config.Provider, entry *reqlog.Entry, start time.Time) {
	rc := http.NewResponseController(w)
	w.WriteHeader(entry.Status)

	sc := newLineScanner()
	buf := make([]byte, 16*1024)
	var tail bytes.Buffer // 保留末尾若干字节用于日志
	var finalUsage *usage
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				break // 客户端断开
			}
			_ = rc.Flush()
			for _, line := range sc.feed(buf[:n]) {
				if u := parseUsageLine(line); u != nil {
					finalUsage = u
				}
			}
			tail.Reset()
			if n > 4096 {
				tail.Write(buf[n-4096 : n])
			} else {
				tail.Write(buf[:n])
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				entry.Error = "流中断: " + rerr.Error()
			}
			break
		}
	}
	_ = rc.Flush()

	if finalUsage != nil {
		entry.PromptTokens = finalUsage.totalIn()
		entry.CompletionTokens = finalUsage.totalOut()
	}
	entry.DurationMs = time.Since(start).Milliseconds()
	entry.Time = start.Format("2006-01-02 15:04:05.000")
	entry.ResponseBody = tail.String()
	s.finish(p, entry)
}

// finish 记录日志与用量。
func (s *Server) finish(p *config.Provider, entry *reqlog.Entry) {
	s.log.Log(*entry)
	cost := 0.0
	if price, ok := p.Prices[entry.Model]; ok {
		cost = float64(entry.PromptTokens)/1e6*price.InputPerMillionTokens +
			float64(entry.CompletionTokens)/1e6*price.OutputPerMillionTokens
	}
	s.stats.Record(p.Name, entry.Model, entry.PromptTokens, entry.CompletionTokens, cost, entry.Status >= 400 || entry.Error != "")
}

// lineScanner 跨读块按行切分。
type lineScanner struct {
	leftover []byte
}

func newLineScanner() *lineScanner { return &lineScanner{} }

func (ls *lineScanner) feed(chunk []byte) [][]byte {
	var lines [][]byte
	data := append(ls.leftover, chunk...)
	last := 0
	for i, b := range data {
		if b == '\n' {
			line := data[last:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			lines = append(lines, line)
			last = i + 1
		}
	}
	ls.leftover = append([]byte(nil), data[last:]...)
	return lines
}

// parseUsageLine 解析 SSE data: 行中的 usage（兼容顶层与 response.usage）。
func parseUsageLine(line []byte) *usage {
	s := string(line)
	if !strings.HasPrefix(s, "data:") {
		return nil
	}
	payload := strings.TrimSpace(s[len("data:"):])
	if payload == "" || payload == "[DONE]" || len(payload) > 1<<20 {
		return nil
	}
	var target scanTarget
	if err := json.Unmarshal([]byte(payload), &target); err != nil {
		return nil
	}
	if target.Usage != nil {
		return target.Usage
	}
	if target.Response != nil && target.Response.Usage != nil {
		return target.Response.Usage
	}
	return nil
}
