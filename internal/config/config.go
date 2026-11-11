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
	// ModelMaxOutput 各真实模型的最大输出 token 数。请求中的 max_tokens /
	// max_completion_tokens 超出该值时被钳制到上限：Copilot CLI 每请求固定
	// 发送 max_tokens，超过真实模型输出上限时部分上游直接返回 400。
	// 0/未配置 = 不限制。
	ModelMaxOutput map[string]int `json:"modelMaxOutput,omitempty"`
	// MaxPromptTokens 注入 COPILOT_PROVIDER_MAX_PROMPT_TOKENS 的供应商级覆盖值
	// （Copilot CLI 启动时读取一次，驱动自动压缩阈值 ≈ 95%）。
	// 0 = 按主对话路由目标的上下文窗口自动推导（见 MaxPromptTokensForBYOK）。
	MaxPromptTokens int               `json:"maxPromptTokens,omitempty"`
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
	// Quota 用量查询配置（空 = 不查询）。配置后 `cops quota`、Web 管理页
	// 可展示该供应商各窗口（5h/周/月等）的限额使用比例，自动切换也依赖它。
	Quota *QuotaConfig `json:"quota,omitempty"`
}

// QuotaConfig 供应商用量查询配置。订阅制供应商（如 GLM Coding Plan）大多有
// 5 小时窗口与周/月窗口等多重限额，查询结果为多窗口列表，切换判定取
// 占比最高的窗口（约束最紧的一个）。
type QuotaConfig struct {
	// Kind 查询实现类型："" = 按供应商 BaseURL 自动探测；"none" = 显式关闭；
	// "glm" = 智谱 GLM Coding Plan（open.bigmodel.cn / z.ai 域名自动适配）。
	Kind string `json:"kind,omitempty"`
	// APIKey 查询用密钥覆盖（部分供应商查询端点与对话端点密钥不同）；
	// 空 = 复用供应商 APIKey。
	APIKey string `json:"apiKey,omitempty"`
	// BaseURL 查询端点根地址覆盖；空 = 按供应商 BaseURL 域名推导。
	BaseURL string `json:"baseUrl,omitempty"`
	// OrganizationID / ProjectID 团队版（组织）查询所需的标识，个人版留空。
	OrganizationID string `json:"organizationId,omitempty"`
	ProjectID      string `json:"projectId,omitempty"`
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
	AutoSwitch   AutoSwitchConfig `json:"autoSwitch"`
}

// AutoSwitchConfig 限额自动切换配置：守护进程周期查询各供应商用量，
// 当前激活供应商任一窗口用量占比达到阈值时，按候选顺序切到第一个
// 未超阈值的供应商。
type AutoSwitchConfig struct {
	Enabled bool `json:"enabled"`
	// ThresholdPct 触发阈值（百分比）；<=0 时取 DefaultAutoSwitchThresholdPct。
	ThresholdPct float64 `json:"thresholdPct,omitempty"`
	// IntervalSec 用量轮询间隔（秒）；<=0 时取 DefaultAutoSwitchIntervalSec。
	IntervalSec int `json:"intervalSec,omitempty"`
	// Order 候选切换顺序（供应商名，越靠前越优先）；缺省 = 配置文件供应商顺序。
	Order []string `json:"order,omitempty"`
}

const (
	DefaultListen       = "127.0.0.1:8317"
	DefaultVirtualModel = "cops-active"

	// EnvMaxPromptTokens 注入 Copilot CLI 的 BYOK 上下文上限变量名。
	// CLI 进程启动时读取一次（运行中会话不热更），驱动其自动压缩阈值（≈95%）。
	EnvMaxPromptTokens = "COPILOT_PROVIDER_MAX_PROMPT_TOKENS"

	// DefaultAutoSwitchThresholdPct 默认切换阈值：任一窗口用量达到 95%。
	DefaultAutoSwitchThresholdPct = 95
	// DefaultAutoSwitchIntervalSec 默认用量轮询间隔（5 分钟）。
	DefaultAutoSwitchIntervalSec = 300
	// MinAutoSwitchIntervalSec 轮询间隔下限（避免打爆上游用量端点）。
	MinAutoSwitchIntervalSec = 30
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
		cleanPosIntMap(&c.Providers[i].ModelMaxOutput)
		if c.Providers[i].Quota != nil {
			c.Providers[i].Quota.Kind = strings.TrimSpace(c.Providers[i].Quota.Kind)
			c.Providers[i].Quota.BaseURL = strings.TrimRight(strings.TrimSpace(c.Providers[i].Quota.BaseURL), "/")
			if *c.Providers[i].Quota == (QuotaConfig{}) {
				c.Providers[i].Quota = nil // 空配置视为未启用
			}
		}
	}
	c.Routing.Normalize()
	c.AutoSwitch.Normalize(c.Providers)
}

// Normalize 清洗自动切换配置：去空白、剔除指向不存在供应商的候选（幂等）。
func (a *AutoSwitchConfig) Normalize(providers []Provider) {
	for i, n := range a.Order {
		a.Order[i] = strings.TrimSpace(n)
	}
	kept := a.Order[:0]
	for _, n := range a.Order {
		if n == "" {
			continue
		}
		if providers != nil {
			found := false
			for i := range providers {
				if providers[i].Name == n {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if len(kept) == 0 || kept[len(kept)-1] != n { // 去重（保持顺序）
			kept = append(kept, n)
		}
	}
	a.Order = kept
}

// EffectiveThresholdPct 生效的切换阈值（百分比，1-100）。
func (a *AutoSwitchConfig) EffectiveThresholdPct() float64 {
	if a.ThresholdPct > 0 {
		return a.ThresholdPct
	}
	return DefaultAutoSwitchThresholdPct
}

// EffectiveIntervalSec 生效的轮询间隔（秒，不低于 MinAutoSwitchIntervalSec）。
func (a *AutoSwitchConfig) EffectiveIntervalSec() int {
	if a.IntervalSec >= MinAutoSwitchIntervalSec {
		return a.IntervalSec
	}
	if a.IntervalSec > 0 {
		return MinAutoSwitchIntervalSec
	}
	return DefaultAutoSwitchIntervalSec
}

// ScrubProviderReferences 删除供应商后清理路由表与自动切换候选中对它的
// 悬空引用，避免后续 PUT routing / autoswitch 因历史残留校验失败。
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
	for i, n := range c.AutoSwitch.Order {
		if n == name {
			c.AutoSwitch.Order = append(c.AutoSwitch.Order[:i], c.AutoSwitch.Order[i+1:]...)
			break
		}
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
	for m, n := range p.ModelMaxOutput {
		if strings.TrimSpace(m) == "" {
			return fmt.Errorf("模型最大输出存在空模型名")
		}
		if n <= 0 {
			return fmt.Errorf("模型 %q 的最大输出必须为正整数: %d", m, n)
		}
	}
	if p.MaxPromptTokens < 0 {
		return fmt.Errorf("maxPromptTokens 不能为负数: %d", p.MaxPromptTokens)
	}
	if p.Quota != nil {
		if err := p.Quota.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Validate 校验用量查询配置。
func (q *QuotaConfig) Validate() error {
	q.Kind = strings.TrimSpace(q.Kind)
	q.BaseURL = strings.TrimSpace(strings.TrimRight(q.BaseURL, "/"))
	switch q.Kind {
	case "", "none", "glm":
	default:
		return fmt.Errorf("用量查询类型 %q 不支持（可选：glm；留空按 BaseURL 自动探测）", q.Kind)
	}
	if q.BaseURL != "" {
		u, err := url.Parse(q.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("用量查询 Base URL 不合法（需形如 https://host[:port][/path]）: %s", q.BaseURL)
		}
	}
	return nil
}

// cleanModelContext 清洗模型上下文表：trim 键、去除空键与非正值条目（幂等）。
func (p *Provider) cleanModelContext() {
	cleanPosIntMap(&p.ModelContext)
}

// cleanPosIntMap 就地清洗「模型名 → 正整数」映射：trim 键、去除空键与非正值
// 条目（幂等），供 ModelContext / ModelMaxOutput 共用。
func cleanPosIntMap(m *map[string]int) {
	for k, v := range *m {
		nk := strings.TrimSpace(k)
		if nk == "" || v <= 0 {
			delete(*m, k)
			continue
		}
		if nk != k {
			delete(*m, k)
			(*m)[nk] = v
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

// QuotaAPIKey 用量查询密钥：Quota.APIKey 覆盖优先，缺省复用供应商 APIKey。
func (p *Provider) QuotaAPIKey() string {
	if p.Quota != nil && p.Quota.APIKey != "" {
		return p.Quota.APIKey
	}
	return p.APIKey
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
