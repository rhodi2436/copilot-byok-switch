// Package tray 系统托盘常驻 UI：供应商快捷切换、打开管理页、
// 注入开关、退出（恢复环境变量并停止代理）。
// 与进程模型解耦：状态与动作由调用方注入，适配嵌入/附着两种模式。
package tray

import (
	"context"
	_ "embed"
	"log"
	"strings"
	"time"

	"fyne.io/systray"
)

//go:embed icon.ico
var iconBytes []byte

// Icon 内嵌托盘图标（ICO 格式）。
func Icon() []byte { return iconBytes }

// ProviderInfo 托盘展示的供应商摘要。
type ProviderInfo struct {
	Name  string
	Model string
}

// State 托盘状态快照。
type State struct {
	Active    string
	Providers []ProviderInfo
	Injected  bool
}

// Signature 状态签名：变化则重建菜单。
func (s State) Signature() string {
	var b strings.Builder
	b.WriteString("a=")
	b.WriteString(s.Active)
	b.WriteString(";i=")
	if s.Injected {
		b.WriteString("1")
	} else {
		b.WriteString("0")
	}
	for _, p := range s.Providers {
		b.WriteString(";p=")
		b.WriteString(p.Name)
		b.WriteString("/")
		b.WriteString(p.Model)
	}
	return b.String()
}

func (s State) Tooltip() string {
	if s.Active == "" {
		return "cops · 未激活供应商"
	}
	for _, p := range s.Providers {
		if p.Name == s.Active {
			return "cops · " + p.Name + " (" + p.Model + ")"
		}
	}
	return "cops · " + s.Active
}

// Actions 托盘动作（由调用方注入）。
type Actions struct {
	Switch       func(name string) error
	OpenAdmin    func()
	ToggleInject func() error // 幂等切换：已注入→恢复，未注入→注入
	Quit         func()       // 完整退出编排（恢复+停代理+退出托盘）
}

type app struct {
	state  func() State
	act    Actions
	cancel context.CancelFunc
	sig    string
}

// Run 启动托盘（阻塞直至 systray.Quit；Windows 上在主 goroutine 调用）。
func Run(stateFn func() State, act Actions) {
	a := &app{state: stateFn, act: act}
	systray.Run(a.onReady, a.onExit)
}

func (a *app) onReady() {
	systray.SetIcon(Icon())
	a.rebuild()
	go a.poll()
}

func (a *app) onExit() {
	// 退出清理由 Quit action 编排；此处无需处理。
}

// poll 轮询状态：签名变化则重建菜单，否则仅刷新 tooltip。
func (a *app) poll() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("tray poll: %v", r)
				}
			}()
			st := a.state()
			if st.Signature() != a.sig {
				a.rebuild()
			} else {
				systray.SetTooltip(st.Tooltip())
			}
		}()
	}
}

func (a *app) rebuild() {
	if a.cancel != nil {
		a.cancel()
	}
	systray.ResetMenu()
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel

	st := a.state()
	a.sig = st.Signature()
	systray.SetTooltip(st.Tooltip())

	title := systray.AddMenuItem(st.Tooltip(), "")
	title.Disable()

	if len(st.Providers) == 0 {
		m := systray.AddMenuItem("尚未配置供应商，点击打开管理页…", "")
		a.watch(ctx, m, a.act.OpenAdmin)
	} else {
		for _, p := range st.Providers {
			label := p.Name
			if p.Model != "" {
				label += "  (" + p.Model + ")"
			}
			item := systray.AddMenuItem(label, "切换到 "+p.Name)
			if p.Name == st.Active {
				item.Check()
			} else {
				item.Uncheck()
			}
			name := p.Name
			a.watch(ctx, item, func() {
				if err := a.act.Switch(name); err != nil {
					log.Printf("tray switch %s: %v", name, err)
				}
				a.rebuild()
			})
		}
	}

	systray.AddSeparator()
	mAdmin := systray.AddMenuItem("管理页…", "添加/编辑供应商、保存密钥")
	a.watch(ctx, mAdmin, a.act.OpenAdmin)

	injectLabel, injectTip := "停用注入（恢复环境变量）", "还原 COPILOT_* 为注入前的值"
	if !st.Injected {
		injectLabel, injectTip = "启用注入（写入 COPILOT_*）", "将 Copilot CLI 指向本地代理"
	}
	mInj := systray.AddMenuItem(injectLabel, injectTip)
	a.watch(ctx, mInj, func() {
		if err := a.act.ToggleInject(); err != nil {
			log.Printf("tray toggle inject: %v", err)
		}
		a.rebuild()
	})

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出（恢复环境变量并停止代理）", "")
	a.watch(ctx, mQuit, a.act.Quit)
}

// watch 监听菜单项点击；ctx 取消（菜单重建）时停止。
func (a *app) watch(ctx context.Context, item *systray.MenuItem, fn func()) {
	go func() {
		select {
		case <-item.ClickedCh:
			select {
			case <-ctx.Done():
			default:
				fn()
			}
		case <-ctx.Done():
		}
	}()
}
