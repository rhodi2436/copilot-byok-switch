// Package config 负责 ~/.cops/config.json 的模型定义与原子读写。
package config

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ModelPrice 每百万 token 的单价（用于费用估算）。
type ModelPrice struct {
	InputPerMillionTokens  float64 `json:"inputPerMillionTokens"`
	OutputPerMillionTokens float64 `json:"outputPerMillionTokens"`
	Currency               string  `json:"currency,omitempty"`
}

// Provider 一个 OpenAI 兼容上游供应商的完整定义。
type Provider struct {
	Name string `json:"name"`
	// BaseURL 上游根地址（OpenAI SDK 约定，通常含 /v1）。
	// 代理会剥离客户端请求路径的 /v1 前缀后拼接。
	// 例：https://api.openai.com/v1 、https://api.deepseek.com/v1 、
	// https://open.bigmodel.cn/api/paas/v4 、http://localhost:11434/v1
	BaseURL  string `json:"baseUrl"`
	APIKey   string `json:"apiKey,omitempty"`
	Model    string `json:"model,omitempty"` // 激活时代理将请求体中的 model 改写为该值
	Models   []string `json:"models,omitempty"` // /v1/models 合成列表与 Web 下拉
	// ModelContext 各真实模型的上下文窗口（token 数），用于 /v1/models 元数据
	// 与展示；虚拟模型未单独覆盖时回退到这里。
	ModelContext map[string]int `json:"modelContext,omitempty"`
	Prices   map[string]ModelPrice `json:"prices,omitempty"`
	ExtraHeaders map[string]string `json:"extraHeaders,omitempty"`
	TimeoutSec   int    `json:"timeoutSec,omitempty"`    // 上游请求超时（秒），默认 600
	// PassthroughModel 为 true 时不改写 model，直接透传客户端原始模型名。
	PassthroughModel bool `json:"passthroughModel,omitempty"`
	// NoUsageInjection 为 true 时不向流式请求注入 stream_options.include_usage
	// （用于不支持该字段的上游）。
	NoUsageInjection bool `json:"noUsageInjection,omitempty"`
	// StripSampling 为 true 时剥离请求体中的采样参数
	// （temperature/top_p/frequency_penalty/presence_penalty）。
	// Copilot CLI 会强制发送 temperature:0 / top_p:0.95（copilot-cli#4950），
	// 部分上游不接受这些字段，可按供应商开启。
	StripSampling bool `json:"stripSampling,omitempty"`
}

// RequestLogConfig 请求日志配置。
type RequestLogConfig struct {
	Enabled    bool `json:"enabled"`
	MaxBodyKB  int  `json:"maxBodyKB"`  // 单条日志截断的请求/响应体上限（KB）
	RetainDays int  `json:"retainDays"` // 日志保留天数
	MaxFileMB  int  `json:"maxFileMB"`  // 单文件上限，超过则轮转
}

// Config 全局配置。
type Config struct {
	Listen       string           `json:"listen"`       // 代理监听地址
	Active       string           `json:"active"`       // 当前激活的供应商名
	VirtualModel string           `json:"virtualModel"` // 写入 COPILOT_MODEL 的虚拟模型名
	Providers    []Provider       `json:"providers"`
	RequestLog   RequestLogConfig `json:"requestLog"`
	Routing      RoutingConfig    `json:"routing"`
}

const (
	DefaultListen       = "127.0.0.1:8317"
	DefaultVirtualModel = "cops-active"
)

// Dir 配置目录（%USERPROFILE%\.cops）。
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".cops"
	}
	return filepath.Join(home, ".cops")
}

// Path 配置文件完整路径。
func Path() string { return filepath.Join(Dir(), "config.json") }

// StatsPath 用量统计文件路径。
func StatsPath() string { return filepath.Join(Dir(), "stats.json") }

// EnvBackupPath 环境变量注入备份文件路径（存在即"已注入"）。
func EnvBackupPath() string { return filepath.Join(Dir(), "env-backup.json") }

// LogsDir 请求日志目录。
func LogsDir() string { return filepath.Join(Dir(), "logs") }

// Default 返回带默认值的空配置。
func Default() *Config {
	return &Config{
		Listen:       DefaultListen,
		VirtualModel: DefaultVirtualModel,
		RequestLog: RequestLogConfig{
			Enabled:    true,
			MaxBodyKB:  32,
			RetainDays: 7,
			MaxFileMB:  20,
		},
	}
}

// Load 读取配置；文件不存在时返回默认配置。
func Load() (*Config, error) {
	c := Default()
	data, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取配置失败 %s: %w", Path(), err)
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("解析配置失败 %s: %w", Path(), err)
	}
	c.fillDefaults()
	return c, nil
}

func (c *Config) fillDefaults() {
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if c.VirtualModel == "" {
		c.VirtualModel = DefaultVirtualModel
	}
	if c.RequestLog.MaxBodyKB <= 0 {
		c.RequestLog.MaxBodyKB = 32
	}
	if c.RequestLog.RetainDays <= 0 {
		c.RequestLog.RetainDays = 7
	}
	if c.RequestLog.MaxFileMB <= 0 {
		c.RequestLog.MaxFileMB = 20
	}
	for i := range c.Providers {
		c.Providers[i].cleanModelContext()
	}
	c.Routing.Normalize()
}

// ScrubProviderReferences 删除供应商后清理路由表中对它的悬空引用，
// 避免后续 PUT routing 因历史残留校验失败。
func (c *Config) ScrubProviderReferences(name string) {
	r := &c.Routing
	for k, t := range r.VirtualModels {
		if t.Provider == name {
			delete(r.VirtualModels, k)
		}
	}
	if r.DefaultVirtual != "" {
		if _, ok := r.VirtualModels[r.DefaultVirtual]; !ok {
			r.DefaultVirtual = ""
		}
	}
	if r.Utility != nil && r.Utility.Provider == name {
		r.Utility = nil
	}
}

// Save 原子保存配置：先写临时文件再替换，并备份旧文件为 config.json.bak。
func (c *Config) Save() error {
	c.fillDefaults()
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	if _, err := os.Stat(Path()); err == nil {
		if bak, err := os.ReadFile(Path()); err == nil {
			_ = os.WriteFile(Path()+".bak", bak, 0o600)
		}
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, Path()); err != nil {
		return fmt.Errorf("替换配置文件失败: %w", err)
	}
	return nil
}

// ParseContextWindow 解析上下文窗口大小，支持 128k / 1.5m / 128000 写法
// （k=1000、m=1_000_000，大小写不限），返回正整数 token 数。
func ParseContextWindow(s string) (int, error) {
	orig := strings.TrimSpace(s)
	v := strings.ToLower(orig)
	if v == "" {
		return 0, fmt.Errorf("上下文窗口不能为空（示例：128k、1.5m、200000）")
	}
	mult := 1
	switch {
	case strings.HasSuffix(v, "k"):
		mult, v = 1_000, strings.TrimSuffix(v, "k")
	case strings.HasSuffix(v, "m"):
		mult, v = 1_000_000, strings.TrimSuffix(v, "m")
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("上下文窗口 %q 不合法（示例：128k、1.5m、200000）", orig)
	}
	n := int(math.Round(f * float64(mult)))
	if n <= 0 {
		return 0, fmt.Errorf("上下文窗口 %q 不合法（示例：128k、1.5m、200000）", orig)
	}
	return n, nil
}

// Validate 校验供应商字段合法性。
func (p *Provider) Validate() error {
	p.Name = strings.TrimSpace(p.Name)
	p.BaseURL = strings.TrimSpace(strings.TrimRight(p.BaseURL, "/"))
	if p.Name == "" {
		return fmt.Errorf("供应商名称不能为空")
	}
	if strings.ContainsAny(p.Name, " \t\"'\\/") {
		return fmt.Errorf("供应商名称不能包含空白或 \\ / \" ' 字符: %s", p.Name)
	}
	if p.BaseURL == "" {
		return fmt.Errorf("Base URL 不能为空")
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("Base URL 不合法（需形如 https://host[:port][/path]）: %s", p.BaseURL)
	}
	for m, n := range p.ModelContext {
		if strings.TrimSpace(m) == "" {
			return fmt.Errorf("模型上下文存在空模型名")
		}
		if n <= 0 {
			return fmt.Errorf("模型 %q 的上下文窗口必须为正整数: %d", m, n)
		}
	}
	return nil
}

// cleanModelContext 清洗模型上下文表：trim 键、去除空键与非正值条目（幂等）。
func (p *Provider) cleanModelContext() {
	for k, v := range p.ModelContext {
		nk := strings.TrimSpace(k)
		if nk == "" || v <= 0 {
			delete(p.ModelContext, k)
			continue
		}
		if nk != k {
			delete(p.ModelContext, k)
			p.ModelContext[nk] = v
		}
	}
}

// Find 按名称查找供应商。
func (c *Config) Find(name string) (*Provider, int) {
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			return &c.Providers[i], i
		}
	}
	return nil, -1
}

// ActiveProvider 返回当前激活的供应商；未设置或不存在时返回 nil。
func (c *Config) ActiveProvider() *Provider {
	if c.Active == "" {
		return nil
	}
	p, _ := c.Find(c.Active)
	return p
}

// TimeoutOrDefault 上游超时秒数（默认 600）。
func (p *Provider) TimeoutOrDefault() int {
	if p.TimeoutSec > 0 {
		return p.TimeoutSec
	}
	return 600
}

// MaskKey 脱敏展示 API Key。
func MaskKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// PublicURL 根据监听地址推导 Copilot CLI 应使用的 base URL。
func (c *Config) PublicURL() string {
	listen := c.Listen
	if listen == "" {
		listen = DefaultListen
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://" + listen + "/v1"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + host + ":" + port + "/v1"
}

// LoadBackup 读取 config.json.bak（用于回滚诊断）。
func LoadBackup() (*Config, error) {
	c := Default()
	data, err := os.ReadFile(Path() + ".bak")
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, err
	}
	return c, nil
}
