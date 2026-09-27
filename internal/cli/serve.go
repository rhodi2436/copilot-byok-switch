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

// daemon 内嵌代理守护进程（serve 与 tray 共用）。
type daemon struct {
	cfgPtr *atomic.Pointer[config.Config]
	admin  *admin.Server
	stats  *stats.Tracker
	log    *reqlog.Logger
	srv    *http.Server
}

// newDaemon 组装守护进程（不监听；serve() 才阻塞监听）。
func newDaemon(cfg *config.Config, logToFile bool) (*daemon, error) {
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
		return nil, fmt.Errorf("打开请求日志失败: %w", err)
	}

	admin.Version = Version
	adminSrv := admin.New(&cfgPtr, st, lg)
	px := proxy.New(&cfgPtr, st, lg)

	mux := http.NewServeMux()
	adminSrv.Register(mux)
	mux.Handle("/v1/", px)
	mux.HandleFunc("/", admin.RootHandler(func() string { return cfgPtr.Load().PublicURL() }))

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 30 * time.Second,
	}

	d := &daemon{cfgPtr: &cfgPtr, admin: adminSrv, stats: st, log: lg, srv: srv}
	adminSrv.OnShutdown = func() {
		_ = st.Flush()
		_ = srv.Close()
	}

	if logToFile {
		f, err := os.OpenFile(filepath.Join(config.LogsDir(), "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			log.SetOutput(f)
		}
	}
	return d, nil
}

// serve 阻塞监听；被 Close 关停时返回 nil。
func (d *daemon) serve() error {
	log.Printf("cops v%s 监听 %s", Version, d.srv.Addr)
	err := d.srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// stopDaemon 关停并落盘。
func (d *daemon) stopDaemon() {
	_ = d.stats.Flush()
	_ = d.srv.Close()
	d.log.Close()
}

var serveHeadless bool

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "启动代理守护进程（前台运行；无托盘）",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if !serveHeadless {
			log.SetFlags(log.LstdFlags)
			fmt.Printf("cops v%s 已启动\n  代理端点: http://%s/v1 （写入 COPILOT_PROVIDER_BASE_URL）\n  管理页面: http://%s/_cops/\n  配置文件: %s\n",
				Version, cfg.Listen, cfg.Listen, config.Path())
			if cfg.ActiveProvider() == nil {
				fmt.Println("  ⚠ 尚未激活供应商：请运行 cops add 添加后 cops switch <名称> 启用")
			} else {
				fmt.Printf("  当前供应商: %s（模型 %s）\n", cfg.Active, cfg.ActiveProvider().Model)
			}
		}
		d, err := newDaemon(cfg, serveHeadless)
		if err != nil {
			return err
		}
		defer d.log.Close()
		return d.serve()
	},
}

func init() {
	serveCmd.Flags().BoolVar(&serveHeadless, "headless", false, "静默模式（日志写入文件）")
	rootCmd.AddCommand(serveCmd)
}
