// glm.go 智谱 GLM Coding Plan 用量查询。
//
// 端点为智谱控制台内部接口 GET /api/monitor/usage/quota/limit（官方 Claude Code
// 插件 zai-coding-plugins 同款，未列入公开 API 文档）：
//
//	国内 https://open.bigmodel.cn/api/monitor/usage/quota/limit
//	国际 https://api.z.ai/api/monitor/usage/quota/limit
//	认证 Authorization: <API Key>（裸 key；上游对 Bearer 形式亦兼容）
//
// 响应（新 credit 套餐示例，旧套餐 type 为 TOKENS_LIMIT 且常有 data 信封差异）：
//
//	{"code":200,"msg":"操作成功","success":true,
//	 "data":{"level":"lite","limits":[
//	   {"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":2000,"currentValue":0,
//	    "remaining":2000,"percentage":0,"nextResetTime":1792674331981},
//	   {"type":"CREDIT_LIMIT","unit":6,"number":1,"usage":10000,"currentValue":3166,
//	    "remaining":6833,"percentage":31,"nextResetTime":1792674331981}]}}
//
// 窗口编码：unit 3 = 小时（number 5 → 5 小时窗）、unit 6 = 周、unit 5 = 月；
// TIME_LIMIT 为 MCP 月度调用次数，不计入套餐额度判定。
// usage = 窗口总额度（credits/积分），currentValue = 已用，percentage = 已用百分比，
// nextResetTime = Unix 毫秒。
package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cops/internal/config"
)

// glmQuotaPath GLM 用量查询端点路径（挂在站点根域名下）。
const glmQuotaPath = "/api/monitor/usage/quota/limit"

// glmBaseURL 推导查询根地址：Quota.BaseURL 覆盖优先，否则取供应商
// BaseURL 的 scheme://host（chat 端点路径不适用于用量端点）。
func glmBaseURL(p *config.Provider) (string, error) {
	if p.Quota != nil && p.Quota.BaseURL != "" {
		return p.Quota.BaseURL, nil
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("无法从 Base URL %q 推导用量查询根地址，可在 quota.baseUrl 覆盖", p.BaseURL)
	}
	return u.Scheme + "://" + u.Host, nil
}

// glmLimit 上游单窗口限额条目。
type glmLimit struct {
	Type         string      `json:"type"`
	Unit         int         `json:"unit"`
	Number       int         `json:"number"`
	Usage        float64     `json:"usage"`        // 窗口总额度（credits / 次数）
	CurrentValue float64     `json:"currentValue"` // 已用
	Remaining    float64     `json:"remaining"`
	Percentage   json.Number `json:"percentage"` // 已用百分比（个别上游返回字符串）
	NextResetMs  int64       `json:"nextResetTime"`
}

// glmResp 上游响应。历史上存在「data 信封」与「顶层直接给 limits」两种形态，
// 两者都兼容；success/code 仅在明确失败时作为错误依据。
type glmResp struct {
	Code    int    `json:"code"`
	Msg     string `json:"msg"`
	Success *bool  `json:"success"`
	Data    *struct {
		Limits []glmLimit `json:"limits"`
		Level  string     `json:"level"`
	} `json:"data"`
	Limits []glmLimit `json:"limits"`
	Level  string     `json:"level"`
}

// glmWindowType unit/number → 规范窗口标识（未知组合原样描述，不臆造）。
func glmWindowType(l glmLimit) string {
	switch l.Unit {
	case 3: // 小时
		if l.Number == 5 {
			return "five_hour"
		}
		return fmt.Sprintf("%dh", l.Number)
	case 6:
		return "weekly"
	case 5:
		return "monthly"
	}
	return fmt.Sprintf("unit%d-%d", l.Unit, l.Number)
}

// queryGLM 查询 GLM Coding Plan 用量。
func queryGLM(ctx context.Context, hc *http.Client, p *config.Provider) (*Usage, error) {
	base, err := glmBaseURL(p)
	if err != nil {
		return nil, err
	}
	endpoint := base + glmQuotaPath
	if p.Quota != nil && p.Quota.OrganizationID != "" && p.Quota.ProjectID != "" {
		endpoint += "?type=2" // 团队版需显式指定组织/项目维度
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	// 官方插件发送裸 key；上游对 Bearer 前缀同样兼容。
	req.Header.Set("Authorization", p.QuotaAPIKey())
	req.Header.Set("Accept", "application/json")
	if p.Quota != nil && p.Quota.OrganizationID != "" && p.Quota.ProjectID != "" {
		req.Header.Set("bigmodel-organization", p.Quota.OrganizationID)
		req.Header.Set("bigmodel-project", p.Quota.ProjectID)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 %s 失败: %w", endpoint, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var gr glmResp
	if err := json.Unmarshal(body, &gr); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w（%s）", err, truncate(string(body), 200))
	}
	if gr.Code != 0 && gr.Code != 200 {
		return nil, fmt.Errorf("上游返回错误 %d: %s", gr.Code, firstNonEmptyStr(gr.Msg, "未知错误"))
	}
	if gr.Success != nil && !*gr.Success {
		return nil, fmt.Errorf("上游返回失败: %s", firstNonEmptyStr(gr.Msg, "未知错误"))
	}
	limits := gr.Limits
	if gr.Data != nil && len(gr.Data.Limits) > 0 {
		limits = gr.Data.Limits
	}
	if len(limits) == 0 {
		return nil, fmt.Errorf("响应不含限额数据（团队版需配置 quota.organizationId/projectId；个人版 key 需为 Coding Plan 套餐 key）")
	}
	out := &Usage{}
	for _, l := range limits {
		// TIME_LIMIT 为 MCP 调用次数限制，不计入套餐额度窗口。
		if l.Type == "TIME_LIMIT" {
			continue
		}
		w := Window{Type: glmWindowType(l)}
		if l.Usage > 0 {
			w.Used, w.Limit = l.CurrentValue, l.Usage
		} else if pct, err := l.Percentage.Float64(); err == nil && pct > 0 {
			w.Used, w.Limit = pct, 100 // 仅有百分比时归一为百分制
		}
		if l.NextResetMs > 0 {
			w.ResetAt = time.UnixMilli(l.NextResetMs)
		}
		out.Windows = append(out.Windows, w)
	}
	if len(out.Windows) == 0 {
		return nil, fmt.Errorf("响应未包含可识别的额度窗口（limits: %s）", truncate(string(body), 200))
	}
	return out, nil
}

func firstNonEmptyStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
