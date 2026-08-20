//go:build !windows

package services

import (
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

type wailsPetWindowDriver struct {
	mu             sync.Mutex
	app            *application.App
	window         application.Window
	windowVersion  uint64
	closingVersion uint64
	windowClosedFn func()
}

// 非 Windows 没有 Wails Windows 消息循环；保留同名 no-op 让主应用可以统一注册
// application.Options.Windows.WndProcInterceptor，而不会把平台判断扩散到启动编排。
func PetWindowWndProcInterceptor(hwnd uintptr, msg uint32, wParam, lParam uintptr) (uintptr, bool) {
	return 0, false
}

func newPetWindowDriver(app *application.App, options PetWindowOptions) (petWindowDriver, error) {
	if app == nil || app.Window == nil {
		return nil, ErrPetWindowNilApplication
	}
	return &wailsPetWindowDriver{app: app}, nil
}

func (d *wailsPetWindowDriver) SetWindowClosedCallback(callback func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.windowClosedFn = callback
}

func (d *wailsPetWindowDriver) CaptureFocusRestore() {}

// 非 Windows 没有统一的顶层窗口枚举能力；返回不可用快照，让前端继续使用桌面地面。
func (d *wailsPetWindowDriver) GetPlatforms() (PetWindowPlatformSnapshot, error) {
	return PetWindowPlatformSnapshot{}, nil
}

// 非 Windows 没有统一、可靠的系统级最后输入时间；返回 0 并明确报 unsupported，
// 让前端关闭 dozing 增强能力，而不是把缺失能力误判成用户刚刚活动。
func readPetWindowIdleSeconds() (int, error) {
	return 0, ErrPetWindowIdleUnsupported
}

func (d *wailsPetWindowDriver) Open(config petWindowOpenConfig) error {
	d.mu.Lock()
	if d.app == nil || d.app.Window == nil {
		d.mu.Unlock()
		return ErrPetWindowNilApplication
	}
	config, err := resolvePetWindowOverlayConfig(d.app, config)
	if err != nil {
		d.mu.Unlock()
		return err
	}
	if d.window != nil {
		window := d.window
		d.mu.Unlock()
		window.SetAlwaysOnTop(config.AlwaysOnTop)
		if err := applyPetWindowOpenConfig(window, config); err != nil {
			return err
		}
		if !window.IsVisible() {
			window.Show()
		}
		return nil
	}

	windowOptions := application.WebviewWindowOptions{
		Name:             config.Name,
		Title:            config.Title,
		Width:            config.Width,
		Height:           config.Height,
		AlwaysOnTop:      config.AlwaysOnTop,
		URL:              config.URL,
		DisableResize:    true,
		Frameless:        true,
		BackgroundType:   application.BackgroundTypeTransparent,
		BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		// Open 可能发生在 app.Run 之前；保持可见选项才能让 Wails 延迟创建
		// 后仍按 PetWindow.Open 的语义显示桌宠。未启用配置时根本不会调用 Open。
		Hidden: false,
		// alpha.38 的该字段在 Linux 上是 no-op，在 macOS 上可用；仍保留统一状态语义，
		// 由平台本身决定能否真正穿透，而不是在公共层伪造支持情况。
		IgnoreMouseEvents: config.IgnoreMouseEvents,
	}
	if config.PositionSet {
		windowOptions.InitialPosition = application.WindowXY
		windowOptions.X = config.X
		windowOptions.Y = config.Y
	}

	window := d.app.Window.NewWithOptions(windowOptions)
	d.windowVersion++
	version := d.windowVersion
	window.RegisterHook(events.Common.WindowClosing, func(_ *application.WindowEvent) {
		d.handleWindowClosing(version)
	})
	d.window = window
	d.mu.Unlock()
	window.Show()
	return nil
}

func (d *wailsPetWindowDriver) Close() error {
	d.mu.Lock()
	window := d.window
	if window == nil {
		d.mu.Unlock()
		return nil
	}
	d.closingVersion = d.windowVersion
	d.mu.Unlock()
	window.Close()
	d.mu.Lock()
	if d.window == window && d.closingVersion == d.windowVersion {
		d.window = nil
		d.closingVersion = 0
	}
	d.mu.Unlock()
	return nil
}

func (d *wailsPetWindowDriver) SetIgnoreMouseEvents(ignore bool) error {
	d.mu.Lock()
	window := d.window
	d.mu.Unlock()
	if window != nil {
		window.SetIgnoreMouseEvents(ignore)
	}
	return nil
}

func (d *wailsPetWindowDriver) Focus() error {
	d.mu.Lock()
	window := d.window
	d.mu.Unlock()
	if window == nil {
		return nil
	}
	// alpha.38 的 Focus() 直接调用内部实现，窗口尚未进入 Wails 主线程时会触发 nil 解引用；
	// 先用公共接口确认窗口已显示，宁可返回可识别错误，也不伪造“已聚焦”。
	if !window.IsVisible() {
		return ErrPetWindowNotReady
	}
	window.Focus()
	return nil
}

func (d *wailsPetWindowDriver) ReleaseFocus() error {
	// alpha.38 没有跨平台 Blur；非 Windows 只能恢复点击穿透，不能伪造系统焦点已释放。
	return nil
}

func (d *wailsPetWindowDriver) SetPosition(x, y int) error {
	d.mu.Lock()
	window := d.window
	d.mu.Unlock()
	if window != nil {
		window.SetPosition(x, y)
	}
	return nil
}

func (d *wailsPetWindowDriver) SetSize(width, height int) error {
	d.mu.Lock()
	window := d.window
	d.mu.Unlock()
	if window != nil {
		window.SetSize(width, height)
	}
	return nil
}

func (d *wailsPetWindowDriver) SetAlwaysOnTop(alwaysOnTop bool) error {
	d.mu.Lock()
	window := d.window
	d.mu.Unlock()
	if window != nil {
		window.SetAlwaysOnTop(alwaysOnTop)
	}
	return nil
}

func (d *wailsPetWindowDriver) SetPlatformLayer(_ string) (bool, error) {
	// 非 Windows 没有统一的跨窗口 Z 序查询；保持普通层级，不伪造 topmost。
	return false, nil
}

func (d *wailsPetWindowDriver) IsFocused() bool {
	d.mu.Lock()
	window := d.window
	d.mu.Unlock()
	return window != nil && window.IsFocused()
}

func (d *wailsPetWindowDriver) handleWindowClosing(version uint64) {
	d.mu.Lock()
	if d.window == nil || d.windowVersion != version {
		d.mu.Unlock()
		return
	}
	d.window = nil
	wasExplicitClose := d.closingVersion == version
	if wasExplicitClose {
		d.closingVersion = 0
	}
	callback := d.windowClosedFn
	d.mu.Unlock()
	if !wasExplicitClose && callback != nil {
		callback()
	}
}
