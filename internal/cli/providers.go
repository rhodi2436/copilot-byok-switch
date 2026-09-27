package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"cops/internal/config"
)

// daemonAPI 探测守护进程是否在运行。
func daemonAPI() (string, bool) {
	cfg, err := config.Load()
	if err != nil {
		return "", false
	}
	url := "http://" + cfg.Listen + "/_cops/api/status"
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(url)
	if err != nil || resp.StatusCode != http.StatusOK {
		return "", false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return "http://" + cfg.Listen, true
}

// prompt 若值非空直接返回，否则在终端上提示输入。
func prompt(reader *bufio.Reader, label, def string) string {
	if def != "" {
		return def
	}
	fmt.Printf("%s: ", label)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

func printTable(headers []string, rows [][]string) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = displayWidth(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if w := displayWidth(c); w > widths[i] {
				widths[i] = w
			}
		}
	}
	var head, sep []string
	for i, h := range headers {
		head = append(head, pad(h, widths[i]))
		sep = append(sep, strings.Repeat("-", widths[i]))
	}
	fmt.Println(strings.Join(head, "  "))
	fmt.Println(strings.Join(sep, "  "))
	for _, r := range rows {
		var cells []string
		for i, c := range r {
			cells = append(cells, pad(c, widths[i]))
		}
		fmt.Println(strings.Join(cells, "  "))
	}
}

// 中文对齐：按显示宽度（CJK 记 2 列）填充。
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if r > 0x2E7F { // CJK 及全角
			w += 2
		} else {
			w++
		}
	}
	return w
}

func pad(s string, width int) string {
	d := width - displayWidth(s)
	if d <= 0 {
		return s
	}
	return s + strings.Repeat(" ", d)
}

var addCmd = &cobra.Command{
	Use:   "add [名称]",
	Short: "添加供应商（OpenAI 兼容上游）",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		reader := bufio.NewReader(os.Stdin)
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		name = prompt(reader, "供应商名称（如 glm）", name)
		url, _ := cmd.Flags().GetString("url")
		url = prompt(reader, "Base URL（如 https://open.bigmodel.cn/api/paas/v4）", url)
		key, _ := cmd.Flags().GetString("key")
		key = prompt(reader, "API Key", key)
		model, _ := cmd.Flags().GetString("model")
		model = prompt(reader, "默认模型", model)

		p := config.Provider{Name: name, BaseURL: url, APIKey: key, Model: model}
		if models, _ := cmd.Flags().GetStringSlice("models"); len(models) > 0 {
			p.Models = models
		} else {
			p.Models = []string{model}
		}
		if pin, _ := cmd.Flags().GetFloat64("price-in"); pin > 0 || func() bool { po, _ := cmd.Flags().GetFloat64("price-out"); return po > 0 }() {
			po, _ := cmd.Flags().GetFloat64("price-out")
			p.Prices = map[string]config.ModelPrice{model: {InputPerMillionTokens: pin, OutputPerMillionTokens: po, Currency: "CNY"}}
		}
		if t, _ := cmd.Flags().GetInt("timeout"); t > 0 {
			p.TimeoutSec = t
		}
		if err := p.Validate(); err != nil {
			return err
		}
		if _, idx := cfg.Find(p.Name); idx >= 0 {
			return fmt.Errorf("供应商已存在: %s（如需更新请在 Web 管理页编辑）", p.Name)
		}
		if len(cfg.Providers) == 0 {
			cfg.Active = p.Name
		}
		cfg.Providers = append(cfg.Providers, p)
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("✅ 已添加供应商 %s", p.Name)
		if cfg.Active == p.Name {
			fmt.Printf(" 并自动激活")
		}
		fmt.Println()
		fmt.Println("   提示: cops switch " + p.Name + " 可随时切换；Web 管理页可编辑详细参数。")
		return nil
	},
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "列出全部供应商",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		rows := [][]string{}
		for _, p := range cfg.Providers {
			mark := "  "
			if p.Name == cfg.Active {
				mark = "●"
			}
			rows = append(rows, []string{mark, p.Name, p.BaseURL, p.Model, config.MaskKey(p.APIKey), fmt.Sprint(len(p.Models))})
		}
		if len(rows) == 0 {
			fmt.Println("暂无供应商，运行 cops add 添加。")
			return nil
		}
		printTable([]string{"", "名称", "Base URL", "默认模型", "API Key", "模型数"}, rows)
		fmt.Println("\n● = 当前激活")
		return nil
	},
}

var currentCmd = &cobra.Command{
	Use:   "current",
	Short: "查看当前激活的供应商",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		p := cfg.ActiveProvider()
		if p == nil {
			fmt.Println("未激活任何供应商。运行 cops list 查看，cops switch <名称> 激活。")
			return nil
		}
		fmt.Printf("供应商:  %s\nBase URL: %s\n模型:    %s\nAPI Key:  %s\n监听:    %s\n",
			p.Name, p.BaseURL, p.Model, config.MaskKey(p.APIKey), cfg.Listen)
		if _, running := daemonAPI(); running {
			fmt.Println("守护进程: 运行中 ✅")
		} else {
			fmt.Println("守护进程: 未运行（cops serve 或重启机器后自启）")
		}
		return nil
	},
}

var switchCmd = &cobra.Command{
	Use:   "switch <名称>",
	Short: "切换激活供应商（守护进程运行中时即时生效，无需重启 Copilot CLI）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		model, _ := cmd.Flags().GetString("model")

		if base, ok := daemonAPI(); ok {
			body, _ := json.Marshal(map[string]string{"name": name, "model": model})
			resp, err := (&http.Client{Timeout: 5 * time.Second}).Post(base+"/_cops/api/switch", "application/json", bytes.NewReader(body))
			if err != nil {
				return fmt.Errorf("调用守护进程失败: %w", err)
			}
			defer resp.Body.Close()
			var out struct {
				OK    bool   `json:"ok"`
				Error string `json:"error"`
				Model string `json:"model"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&out)
			if resp.StatusCode != http.StatusOK || !out.OK {
				return fmt.Errorf("切换失败: %s", out.Error)
			}
			fmt.Printf("✅ 已切换到 %s（模型 %s）—— 即时生效，无需重启 Copilot CLI\n", name, out.Model)
			return nil
		}

		cfg, err := config.Load()
		if err != nil {
			return err
		}
		p, _ := cfg.Find(name)
		if p == nil {
			return fmt.Errorf("供应商不存在: %s（cops list 查看）", name)
		}
		if model != "" {
			p.Model = model
		}
		cfg.Active = name
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("✅ 已切换到 %s（模型 %s）。\n   守护进程未运行：下次 cops serve 启动后生效。\n", name, p.Model)
		return nil
	},
}

var removeCmd = &cobra.Command{
	Use:   "remove <名称>",
	Short: "删除供应商",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		_, idx := cfg.Find(args[0])
		if idx < 0 {
			return fmt.Errorf("供应商不存在: %s", args[0])
		}
		cfg.Providers = append(cfg.Providers[:idx], cfg.Providers[idx+1:]...)
		if cfg.Active == args[0] {
			cfg.Active = ""
			if len(cfg.Providers) > 0 {
				cfg.Active = cfg.Providers[0].Name
			}
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("✅ 已删除 %s；当前激活: %s\n", args[0], cfg.Active)
		return nil
	},
}

var testCmd = &cobra.Command{
	Use:   "test [名称]",
	Short: "连通性测试（调用上游 /v1/models）",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		name := cfg.Active
		if len(args) > 0 {
			name = args[0]
		}
		p, _ := cfg.Find(name)
		if p == nil {
			return fmt.Errorf("供应商不存在或未激活: %s", name)
		}
		client := &http.Client{Timeout: 15 * time.Second}
		req, _ := http.NewRequest("GET", p.BaseURL+"/models", nil)
		if p.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+p.APIKey)
		}
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("❌ %s 连接失败: %v\n", p.Name, err)
			return nil
		}
		defer resp.Body.Close()
		lat := time.Since(start).Round(time.Millisecond)
		if resp.StatusCode != http.StatusOK {
			fmt.Printf("❌ %s 返回 HTTP %d（%s）\n", p.Name, resp.StatusCode, lat)
			return nil
		}
		var out struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = json.Unmarshal(body, &out)
		fmt.Printf("✅ %s 连通（%s，/v1/models 返回 %d 个模型）\n", p.Name, lat, len(out.Data))
		return nil
	},
}

func init() {
	addCmd.Flags().String("url", "", "Base URL")
	addCmd.Flags().String("key", "", "API Key")
	addCmd.Flags().String("model", "", "默认模型")
	addCmd.Flags().StringSlice("models", nil, "模型清单（逗号分隔）")
	addCmd.Flags().Float64("price-in", 0, "每百万输入 token 单价")
	addCmd.Flags().Float64("price-out", 0, "每百万输出 token 单价")
	addCmd.Flags().Int("timeout", 0, "上游超时秒数")
	switchCmd.Flags().String("model", "", "同时更新该供应商的默认模型")
	rootCmd.AddCommand(addCmd, listCmd, currentCmd, switchCmd, removeCmd, testCmd)
}
