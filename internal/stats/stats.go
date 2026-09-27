// Package stats 按 日期/供应商/模型 聚合 token 用量与估算费用。
package stats

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"
)

// Key 聚合维度。
type Key struct {
	Date     string `json:"date"`     // YYYY-MM-DD（本地时区）
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// Entry 单维度累计值。
type Entry struct {
	Requests      int64   `json:"requests"`
	Errors        int64   `json:"errors"`
	InputTokens   int64   `json:"inputTokens"`
	OutputTokens  int64   `json:"outputTokens"`
	Cost          float64 `json:"cost"`
}

// Row 对外展示行。
type Row struct {
	Key
	Entry
}

type fileData struct {
	Rows []Row `json:"rows"`
}

// Tracker 内存聚合 + 去抖落盘（~/.cops/stats.json）。
type Tracker struct {
	mu       sync.Mutex
	data     map[Key]*Entry
	path     string
	lastSave time.Time
	dirty    bool
}

// New 创建 Tracker 并加载既有数据。
func New(path string) *Tracker {
	t := &Tracker{data: map[Key]*Entry{}, path: path}
	if raw, err := os.ReadFile(path); err == nil {
		var fd fileData
		if json.Unmarshal(raw, &fd) == nil {
			for _, r := range fd.Rows {
				cp := r.Entry
				t.data[r.Key] = &cp
			}
		}
	}
	return t
}

// Record 记录一次请求的用量；cost 由调用方按价格表算好传入。
func (t *Tracker) Record(provider, model string, input, output int64, cost float64, isErr bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := Key{Date: time.Now().Format("2006-01-02"), Provider: provider, Model: model}
	e := t.data[k]
	if e == nil {
		e = &Entry{}
		t.data[k] = e
	}
	e.Requests++
	if isErr {
		e.Errors++
	}
	e.InputTokens += input
	e.OutputTokens += output
	e.Cost += cost
	t.dirty = true
	// 去抖：距上次保存超过 3 秒才同步落盘（文件很小，可接受）。
	if time.Since(t.lastSave) > 3*time.Second {
		_ = t.saveLocked()
	}
}

// Rows 返回最近 days 天的明细（按日期倒序）。
func (t *Tracker) Rows(days int) []Row {
	t.mu.Lock()
	defer t.mu.Unlock()
	cutoff := time.Now().AddDate(0, 0, -days + 1).Format("2006-01-02")
	var rows []Row
	for k, e := range t.data {
		if k.Date >= cutoff {
			rows = append(rows, Row{Key: k, Entry: *e})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Date != rows[j].Date {
			return rows[i].Date > rows[j].Date
		}
		if rows[i].Provider != rows[j].Provider {
			return rows[i].Provider < rows[j].Provider
		}
		return rows[i].Model < rows[j].Model
	})
	return rows
}

// Summary 按供应商+模型汇总最近 days 天。
func (t *Tracker) Summary(days int) []Row {
	rows := t.Rows(days)
	agg := map[Key]*Entry{}
	for _, r := range rows {
		k := Key{Provider: r.Provider, Model: r.Model}
		e := agg[k]
		if e == nil {
			e = &Entry{}
			agg[k] = e
		}
		e.Requests += r.Requests
		e.Errors += r.Errors
		e.InputTokens += r.InputTokens
		e.OutputTokens += r.OutputTokens
		e.Cost += r.Cost
	}
	out := make([]Row, 0, len(agg))
	for k, e := range agg {
		out = append(out, Row{Key: k, Entry: *e})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// Flush 强制落盘（供关停时调用）。
func (t *Tracker) Flush() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.saveLocked()
}

func (t *Tracker) saveLocked() error {
	rows := make([]Row, 0, len(t.data))
	for k, e := range t.data {
		rows = append(rows, Row{Key: k, Entry: *e})
	}
	data, err := json.MarshalIndent(fileData{Rows: rows}, "", "  ")
	if err != nil {
		return err
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, t.path); err != nil {
		return err
	}
	t.lastSave = time.Now()
	t.dirty = false
	return nil
}
