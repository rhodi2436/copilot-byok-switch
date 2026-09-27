package cli

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"cops/internal/admin"
	"cops/internal/config"
	"cops/internal/proxy"
	"cops/internal/reqlog"
	"cops/internal/stats"
)

var serveHeadless bool

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "启动代理守护进程（前台运行；开机自启调用本命令）",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		var cfgPtr atomic.Pointer[config.Config]
		cfgPtr.Store(cfg)

		st := stats.New(config.StatsPath())
		lg, err := reqlog.Open(config.LogsDir(), reqlog.Options{
			Enabled:    cfg.RequestLog.Enabled,
			MaxBodyKB:  cfg.RequestLog.MaxBodyKB,
			RetainDays: cfg.RequestLog.RetainDays,
			MaxFileMB:  cfg.RequestLog.MaxFileMB,
		})
		if err != nil {
			return fmt.Errorf("打开请求日志失败: %w", err)
		}
		defer lg.Close()

		admin.Version = Version
		adminSrv := admin.New(&cfgPtr, st, lg)
		px := proxy.New(&cfgPtr, st, lg)

		mux := http.NewServeMux()
		adminSrv.Register(mux)
		mux.Handle("/v1/", px)
		mux.HandleFunc("/", admin.RootHandler(func() string { return cfgPtr.Load().PublicURL() }))

		if !serveHeadless {
			log.SetFlags(log.LstdFlags)
			fmt.Printf("cops v%s 已启动\n  代理端点: http://%s/v1 （写入 COPILOT_PROVIDER_BASE_URL）\n  管理页面: http://%s/_cops/\n  配置文件: %s\n",
				Version, cfg.Listen, cfg.Listen, config.Path())
			if cfg.ActiveProvider() == nil {
				fmt.Println("  ⚠ 尚未激活供应商：请运行 cops add 添加后 cops switch <名称> 启用")
			} else {
				fmt.Printf("  当前供应商: %s（模型 %s）\n", cfg.Active, cfg.ActiveProvider().Model)
			}
		} else {
			log.SetOutput(os.Stderr)
			f, err := os.OpenFile(filepath.Join(config.LogsDir(), "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err == nil {
				log.SetOutput(f)
			}
		}

		srv := &http.Server{
			Addr:              cfg.Listen,
			Handler:           mux,
			ReadHeaderTimeout: 30 * time.Second,
		}
		log.Printf("cops v%s 监听 %s", Version, cfg.Listen)
		return srv.ListenAndServe()
	},
}

func init() {
	serveCmd.Flags().BoolVar(&serveHeadless, "headless", false, "静默模式（自启动用，日志写入文件）")
	rootCmd.AddCommand(serveCmd)
}
