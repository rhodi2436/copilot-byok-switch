// Package reqlog 以 JSONL 记录代理请求明细（调试用），支持大小轮转与按天清理。
package reqlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Entry 单条请求日志。
type Entry struct {
	Time             string `json:"time"`
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	Method           string `json:"method"`
	Path             string `json:"path"`
	Status           int    `json:"status"`
	DurationMs       int64  `json:"durationMs"`
	Stream           bool   `json:"stream"`
	PromptTokens     int64  `json:"promptTokens,omitempty"`
	CompletionTokens int64  `json:"completionTokens,omitempty"`
	Error            string `json:"error,omitempty"`
	RequestBody      string `json:"requestBody,omitempty"`
	ResponseBody     string `json:"responseBody,omitempty"`
}

// Options 日志行为参数（来自 config.RequestLogConfig）。
type Options struct {
	Enabled    bool
	MaxBodyKB  int
	RetainDays int
	MaxFileMB  int
}

// Logger JSONL 写入器。
type Logger struct {
	mu       sync.Mutex
	f        *os.File
	path     string
	size     int64
	opts     Options
	disabled bool
}

// CurrentFile 当前日志文件名。
const CurrentFile = "requests.jsonl"

// Open 打开（或创建）日志文件并执行过期清理。
func Open(dir string, opts Options) (*Logger, error) {
	l := &Logger{path: filepath.Join(dir, CurrentFile), opts: opts}
	if !opts.Enabled {
		l.disabled = true
		return l, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	st, _ := f.Stat()
	l.f = f
	l.size = st.Size()
	l.cleanup(dir)
	return l, nil
}

// Log 追加一条日志（body 超限截断）。
func (l *Logger) Log(e Entry) {
	if l.disabled {
		return
	}
	max := int64(l.opts.MaxBodyKB) * 1024
	if max <= 0 {
		max = 32 * 1024
	}
	e.RequestBody = truncate(e.RequestBody, max)
	e.ResponseBody = truncate(e.ResponseBody, max)
	if e.Time == "" {
		e.Time = time.Now().Format("2006-01-02 15:04:05.000")
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	data = append(data, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	n, _ := l.f.Write(data)
	l.size += int64(n)
	l.rotateIfNeeded()
}

func (l *Logger) rotateIfNeeded() {
	maxBytes := int64(l.opts.MaxFileMB) * 1024 * 1024
	if maxBytes <= 0 || l.size < maxBytes {
		return
	}
	l.f.Close()
	rotated := filepath.Join(filepath.Dir(l.path),
		fmt.Sprintf("requests-%s.jsonl", time.Now().Format("20060102-150405")))
	_ = os.Rename(l.path, rotated)
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		l.f = nil
		return
	}
	l.f = f
	l.size = 0
}

func (l *Logger) cleanup(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -l.opts.RetainDays)
	var names []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "requests-") && strings.HasSuffix(n, ".jsonl") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		info, err := os.Stat(filepath.Join(dir, n))
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}

// Close 关闭底层文件。
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}

func truncate(s string, max int64) string {
	if int64(len(s)) <= max {
		return s
	}
	return s[:max] + "...[截断]"
}

// ReadLast 从 JSONL 文件读取末尾 n 条（保持原顺序）。
func ReadLast(path string, n int) ([]Entry, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var all []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e Entry
		if json.Unmarshal(line, &e) == nil {
			all = append(all, e)
		}
	}
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}
