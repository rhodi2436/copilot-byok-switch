package winenv

// zshrc 标记块读写：macOS 注入的落地实现（nvm/rbenv 风格的标记块）。
// 本文件不带构建标签——纯文件操作，任意平台均可单元测试。
//
// 块格式：
//
//	# >>> cops >>>
//	export KEY='value'
//	# <<< cops <<<
//
// 值采用单引号包裹（zsh 内无插值），值内的单引号按惯例转义为 '\''。

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	blockBegin = "# >>> cops >>>"
	blockEnd   = "# <<< cops <<<"
)

// UpdateShellBlock 将 vars 写入 path 的标记块：块不存在则在文件末尾追加，
// 存在则整块替换；块的其余内容保持不动。键按字典序输出，保证幂等。
func UpdateShellBlock(path string, vars map[string]string) error {
	content := readFileOrEmpty(path)
	updated := replaceBlock(content, renderBlock(vars))
	return atomicWrite(path, updated)
}

// ReadShellBlock 解析标记块内的 export KEY='value'。块不存在时 found 为 false。
// 容忍单引号转义与裸值两种写法。
func ReadShellBlock(path string) (map[string]string, bool) {
	lines := strings.Split(readFileOrEmpty(path), "\n")
	vars, _, found := parseBlock(lines)
	return vars, found
}

// RemoveShellBlock 删除标记块（含起止标记）。文件或块不存在时返回 false。
func RemoveShellBlock(path string) (bool, error) {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	lines := strings.Split(string(content), "\n")
	_, idx, found := parseBlock(lines)
	if !found {
		return false, nil
	}
	kept := append(append([]string{}, lines[:idx.begin]...), lines[idx.end+1:]...)
	// 归一化尾部换行：去掉块与结尾之间的空行残留，保留恰好一个结尾换行
	joined := strings.TrimRight(strings.Join(kept, "\n"), "\n")
	if joined != "" {
		joined += "\n"
	}
	if err := atomicWrite(path, joined); err != nil {
		return false, err
	}
	return true, nil
}

// blockIndex 标记块的行号区间（含起止标记行）。
type blockIndex struct{ begin, end int }

// parseBlock 定位标记块并解析其中变量。begin/end 为含标记的行号。
func parseBlock(lines []string) (map[string]string, blockIndex, bool) {
	begin, end := -1, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if begin < 0 && t == blockBegin {
			begin = i
		} else if begin >= 0 && t == blockEnd {
			end = i
			break
		}
	}
	if begin < 0 || end < 0 {
		return nil, blockIndex{}, false
	}
	vars := make(map[string]string)
	for _, l := range lines[begin+1 : end] {
		k, v, ok := parseExport(l)
		if ok {
			vars[k] = v
		}
	}
	return vars, blockIndex{begin, end}, true
}

// replaceBlock 在整文件内容中替换（或追加）标记块。
func replaceBlock(content, block string) string {
	lines := strings.Split(content, "\n")
	_, idx, found := parseBlock(lines)
	blockLines := strings.Split(block, "\n")
	if !found {
		// 追加到末尾：确保与原内容间恰好一个空行
		trimmed := strings.TrimRight(content, "\n")
		if trimmed == "" {
			return block + "\n"
		}
		return trimmed + "\n\n" + block + "\n"
	}
	kept := append(append([]string{}, lines[:idx.begin]...), blockLines...)
	kept = append(kept, lines[idx.end+1:]...)
	return strings.Join(kept, "\n")
}

// renderBlock 渲染标记块文本（键按字典序）。
func renderBlock(vars map[string]string) string {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(blockBegin + "\n")
	for _, k := range keys {
		b.WriteString("export " + k + "=" + quoteSingle(vars[k]) + "\n")
	}
	b.WriteString(blockEnd)
	return b.String()
}

// quoteSingle 单引号包裹并转义内部单引号（'\'' 惯例）。
func quoteSingle(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// unquoteSingle 还原 quoteSingle 的转义；仅处理我们自己的写法。
func unquoteSingle(v string) (string, bool) {
	if len(v) < 2 || !strings.HasPrefix(v, "'") || !strings.HasSuffix(v, "'") {
		return "", false
	}
	inner := v[1 : len(v)-1]
	return strings.ReplaceAll(inner, `'\''`, "'"), true
}

// parseExport 解析一行 export KEY=VALUE（容忍我们写的单引号格式与裸值）。
func parseExport(line string) (string, string, bool) {
	l := strings.TrimSpace(line)
	if !strings.HasPrefix(l, "export ") {
		return "", "", false
	}
	rest := strings.TrimPrefix(l, "export ")
	eq := strings.IndexByte(rest, '=')
	if eq <= 0 {
		return "", "", false
	}
	key, val := rest[:eq], rest[eq+1:]
	if key == "" || strings.ContainsAny(key, " \t") {
		return "", "", false
	}
	if v, ok := unquoteSingle(val); ok {
		return key, v, true
	}
	return key, val, true // 裸值（非本工具写入的格式），按字面接受
}

// readFileOrEmpty 读文件；不存在返回空串。
func readFileOrEmpty(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// atomicWrite 临时文件 + 原子替换，避免半成品破坏用户 shell 配置。
func atomicWrite(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cops-block-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // rename 成功后为 no-op
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
