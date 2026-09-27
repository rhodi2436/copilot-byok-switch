// Package admin 提供 /_cops/ 下的管理 API 与嵌入式 Web 管理页。
// 与 proxy 共享同一个 atomic.Pointer[config.Config]，切换即时生效。
package admin

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"cops/internal/config"
	"cops/internal/reqlog"
	"cops/internal/stats"
	"cops/internal/winenv"
)

//go:embed all:web
var webFS embed.FS

// Version 在 main 中通过 -ldflags 注入，默认 dev。
var Version = "dev"

// Server 管理接口。
type Server struct {
	cfgPtr *atomic.Pointer[config.Config]
	stats  *stats.Tracker
	log    *reqlog.Logger
	// OnShutdown 由调用方注入：收到 shutdown 请求时优雅关停守护进程。
	OnShutdown func()
}

// New 创建管理服务。
func New(cfgPtr *atomic.Pointer[config.Config], st *stats.Tracker, lg *reqlog.Logger) *Server {
	return &Server{cfgPtr: cfgPtr, stats: st, log: lg}
}

// Register 挂载到 mux。
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("/_cops/api/status", s.handleStatus)
	mux.HandleFunc("/_cops/api/providers", s.handleProviders)
	mux.HandleFunc("/_cops/api/switch", s.handleSwitch)
	mux.HandleFunc("/_cops/api/stats", s.handleStats)
	mux.HandleFunc("/_cops/api/logs", s.handleLogs)
	mux.HandleFunc("/_cops/api/shutdown", s.handleShutdown)
	mux.HandleFunc("/_cops/", s.handleUI)
	mux.HandleFunc("/_cops", s.handleUI)
}

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/_cops/" && r.URL.Path != "/_cops" && r.URL.Path != "/_cops/index.html" {
		http.NotFound(w, r)
		return
	}
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "embedded UI missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfgPtr.Load()
	p := cfg.ActiveProvider()
	resp := map[string]any{
		"version":      Version,
		"listen":       cfg.Listen,
		"active":       cfg.Active,
		"virtualModel": cfg.VirtualModel,
		"copilotEnv": map[string]string{
			"COPILOT_PROVIDER_TYPE":     "openai",
			"COPILOT_PROVIDER_BASE_URL": cfg.PublicURL(),
			"COPILOT_PROVIDER_API_KEY":  "<占位符，真实 Key 由 cops 注入>",
			"COPILOT_MODEL":             cfg.VirtualModel,
		},
	}
	if p != nil {
		resp["activeProvider"] = map[string]any{
			"name": p.Name, "baseUrl": p.BaseURL, "model": p.Model, "models": p.Models,
		}
	}
	_, autostart := winenv.GetAutostart()
	resp["injected"] = winenv.IsInjected(config.EnvBackupPath())
	resp["autostart"] = autostart
	writeJSON(w, http.StatusOK, resp)
}

func sanitize(p config.Provider) config.Provider {
	p.APIKey = config.MaskKey(p.APIKey)
	return p
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfgPtr.Load()
	switch r.Method {
	case http.MethodGet:
		list := make([]config.Provider, 0, len(cfg.Providers))
		for _, p := range cfg.Providers {
			list = append(list, sanitize(p))
		}
		writeJSON(w, http.StatusOK, map[string]any{"providers": list, "active": cfg.Active})
	case http.MethodPost, http.MethodPut:
		var p config.Provider
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err := json.Unmarshal(body, &p); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSON 解析失败: " + err.Error()})
			return
		}
		if err := p.Validate(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if old, idx := cfg.Find(p.Name); idx >= 0 {
			if p.APIKey == "" || strings.HasPrefix(p.APIKey, "***") {
				p.APIKey = old.APIKey // 前端回传脱敏 Key 时保留原值
			}
			cfg.Providers[idx] = p
		} else {
			if len(cfg.Providers) == 0 {
				cfg.Active = p.Name // 第一个供应商自动激活
			}
			cfg.Providers = append(cfg.Providers, p)
		}
		if err := cfg.Save(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		s.cfgPtr.Store(cfg)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "active": cfg.Active})
	case http.MethodDelete:
		name := r.URL.Query().Get("name")
		_, idx := cfg.Find(name)
		if idx < 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "供应商不存在: " + name})
			return
		}
		cfg.Providers = append(cfg.Providers[:idx], cfg.Providers[idx+1:]...)
		if cfg.Active == name {
			cfg.Active = ""
			if len(cfg.Providers) > 0 {
				cfg.Active = cfg.Providers[0].Name
			}
		}
		if err := cfg.Save(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		s.cfgPtr.Store(cfg)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "active": cfg.Active})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// Switch 切换激活供应商（提取为方法，供 HTTP handler 与托盘同进程直调）。
func (s *Server) Switch(name, model string) (*config.Provider, error) {
	cfg := s.cfgPtr.Load()
	p, _ := cfg.Find(name)
	if p == nil {
		return nil, fmt.Errorf("供应商不存在: %s", name)
	}
	if model != "" {
		p.Model = model
	}
	cfg.Active = name
	if err := cfg.Save(); err != nil {
		return nil, err
	}
	s.cfgPtr.Store(cfg)
	return p, nil
}

func (s *Server) handleSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name  string `json:"name"`
		Model string `json:"model"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSON 解析失败"})
		return
	}
	p, err := s.Switch(req.Name, req.Model)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "active": p.Name, "model": p.Model})
}

// handleShutdown 优雅关停守护进程（托盘附着模式退出时调用）。
func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	if s.OnShutdown != nil {
		go func() {
			time.Sleep(300 * time.Millisecond) // 留时间让响应送达
			s.OnShutdown()
		}()
	}
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && v > 0 && v <= 365 {
		days = v
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"summary": s.stats.Summary(days),
		"rows":    s.stats.Rows(days),
	})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 500 {
		limit = v
	}
	entries, err := reqlog.ReadLast(filepath.Join(config.LogsDir(), reqlog.CurrentFile), limit)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"entries": []any{}})
		return
	}
	if entries == nil {
		entries = []reqlog.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// RootHandler 根路径欢迎页（确认代理在运行）。
func RootHandler(cfg func() string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html lang="zh"><body style="font-family:system-ui;padding:3rem">
<h2>cops 代理运行中 ✅</h2>
<p>Copilot CLI base URL: <code>%s</code></p>
<p><a href="/_cops/">打开管理页</a></p>
</body></html>`, cfg())
	}
}
