package cli

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"cops/internal/config"
	"cops/internal/reqlog"
	"cops/internal/stats"
)

var logsFollow bool

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "查看代理请求日志",
	RunE: func(cmd *cobra.Command, args []string) error {
		limit, _ := cmd.Flags().GetInt("limit")
		path := config.LogsDir() + string(os.PathSeparator) + reqlog.CurrentFile
		entries, err := reqlog.ReadLast(path, limit)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Println("暂无日志。")
				return nil
			}
			return err
		}
		if len(entries) == 0 && !logsFollow {
			fmt.Println("暂无日志。")
		}
		for _, e := range entries {
			printLogEntry(e)
		}
		if !logsFollow {
			return nil
		}
		// 简易 tail -f：每秒轮询新增行。
		fmt.Println("— 跟随中（Ctrl+C 退出）—")
		offset, _ := fileSize(path)
		for {
			time.Sleep(time.Second)
			st, err := os.Stat(path)
			if err != nil || st.Size() < offset {
				offset, _ = fileSize(path) // 轮转后重置
				continue
			}
			if st.Size() == offset {
				continue
			}
			f, err := os.Open(path)
			if err != nil {
				continue
			}
			if _, err := f.Seek(offset, 0); err == nil {
				buf := make([]byte, st.Size()-offset)
				if n, _ := f.Read(buf); n > 0 {
					for _, line := range strings.Split(strings.TrimRight(string(buf[:n]), "\n"), "\n") {
						var e reqlog.Entry
						if json.Unmarshal([]byte(strings.TrimSpace(line)), &e) == nil {
							printLogEntry(e)
						}
					}
				}
			}
			f.Close()
			offset = st.Size()
		}
	},
}

func printLogEntry(e reqlog.Entry) {
	status := fmt.Sprint(e.Status)
	if e.Status >= 400 {
		status = "❌ " + status
	} else if e.Status > 0 {
		status = "✅ " + status
	}
	fmt.Printf("%s  %-10s %-22s %-22s %6dms  in=%-6d out=%-6d %s\n",
		e.Time, e.Provider, e.Model, e.Path, e.DurationMs,
		e.PromptTokens, e.CompletionTokens, firstNonEmpty(e.Error, status))
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func fileSize(path string) (int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "查看用量与费用统计",
	RunE: func(cmd *cobra.Command, args []string) error {
		days, _ := cmd.Flags().GetInt("days")
		tr := stats.New(config.StatsPath())
		rows := tr.Summary(days)
		if len(rows) == 0 {
			fmt.Printf("最近 %d 天暂无用量数据。\n", days)
			return nil
		}
		out := [][]string{}
		var totIn, totOut int64
		var totCost float64
		var totReq int64
		for _, r := range rows {
			out = append(out, []string{
				r.Provider, r.Model,
				strconv.FormatInt(r.Requests, 10),
				strconv.FormatInt(r.Errors, 10),
				fmt.Sprintf("%d/%d", r.InputTokens, r.OutputTokens),
				fmt.Sprintf("%.4f", r.Cost),
			})
			totIn += r.InputTokens
			totOut += r.OutputTokens
			totCost += r.Cost
			totReq += r.Requests
		}
		printTable([]string{"供应商", "模型", "请求", "失败", "输入/输出 tokens", "费用"}, out)
		fmt.Printf("\n合计: %d 次请求, 输入 %s tokens, 输出 %s tokens, 估算费用 %.4f\n",
			totReq, strconv.FormatInt(totIn, 10), strconv.FormatInt(totOut, 10), totCost)
		fmt.Println("（费用按配置的单价估算，未配置价格的模型计为 0）")
		return nil
	},
}

var openCmd = &cobra.Command{
	Use:   "open",
	Short: "在浏览器中打开 Web 管理页",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return openBrowser(baseURLOf(cfg) + "/_cops/")
	},
}

// baseURLOf 守护进程对外的访问根地址（通配地址映射为 127.0.0.1）。
func baseURLOf(cfg *config.Config) string {
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return "http://" + cfg.Listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + host + ":" + port
}

// openBrowser 用系统默认浏览器打开 URL。
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func init() {
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "持续跟随新日志")
	logsCmd.Flags().Int("limit", 20, "显示条数")
	statsCmd.Flags().Int("days", 7, "统计天数")
	rootCmd.AddCommand(logsCmd, statsCmd, openCmd)
}
