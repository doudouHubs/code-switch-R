//go:build windows

package services

import (
	"fmt"
	"sync"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"golang.org/x/sys/windows"
)

var (
	user32PetWindow      = windows.NewLazySystemDLL("user32.dll")
	getLastInputInfoProc = user32PetWindow.NewProc("GetLastInputInfo")
	getCursorPosProc     = user32PetWindow.NewProc("GetCursorPos")
	getWindowRectProc    = user32PetWindow.NewProc("GetWindowRect")
	setFocusProc         = user32PetWindow.NewProc("SetFocus")
	setForegroundProc    = user32PetWindow.NewProc("SetForegroundWindow")
	isWindowProc         = user32PetWindow.NewProc("IsWindow")
	kernel32PetWindow    = windows.NewLazySystemDLL("kernel32.dll")
	getTickCount64Proc   = kernel32PetWindow.NewProc("GetTickCount64")
)

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

type petWindowPoint struct {
	x int32
	y int32
}

type petWindowRect struct {
	left   int32
	top    int32
	right  int32
	bottom int32
}

const petWindowPointerEvent = "pet.window.pointer"

func readPetWindowIdleSeconds() (int, error) {
	info := lastInputInfo{cbSize: uint32(unsafe.Sizeof(lastInputInfo{}))}
	result, _, callErr := getLastInputInfoProc.Call(uintptr(unsafe.Pointer(&info)))
	if result == 0 {
		if callErr == windows.ERROR_SUCCESS {
			return 0, fmt.Errorf("GetLastInputInfo returned false")
		}
		return 0, fmt.Errorf("GetLastInputInfo: %w", callErr)
	}

	// GetLastInputInfo.dwTime 是 32 位 tick；用同样的低 32 位做无符号减法，
	// 可以正确处理 Windows tick 在约 49 天后的回绕，且桌宠只关心分钟级空闲阈值。
	ticks, _, _ := getTickCount64Proc.Call()
	elapsedMilliseconds := uint32(uint64(ticks)) - info.dwTime
	return int(elapsedMilliseconds / 1000), nil
}

type wailsPetWindowDriver struct {
	mu             sync.Mutex
	app            *application.App
	window         application.Window
	windowVersion  uint64
	closingVersion uint64
	windowClosedFn func()
	pointerStop    chan struct{}
	focusRestore   windows.HWND
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

func (d *wailsPetWindowDriver) CaptureFocusRestore() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.focusRestore != 0 || d.window == nil {
		return
	}
	petHWND := windows.HWND(uintptr(d.window.NativeWindow()))
	foreground := windows.GetForegroundWindow()
	if foreground != 0 && foreground != petHWND {
		d.focusRestore = foreground
	}
}

func (d *wailsPetWindowDriver) Open(config petWindowOpenConfig) error {
	d.mu.Lock()
	if d.window != nil {
		d.mu.Unlock()
		return nil
	}
	if d.app == nil || d.app.Window == nil {
		d.mu.Unlock()
		return ErrPetWindowNilApplication
	}
	config, err := resolvePetWindowOverlayConfig(d.app, config)
	if err != nil {
		d.mu.Unlock()
		return err
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
		// Open 可能发生在 app.Run 之前。Wails alpha.38 会把窗口创建延迟到
		// Run，但不会把启动前调用的 Show() 语义补写回 options.Hidden；若这里
		// 继续设为 true，启用配置下的桌宠会被创建成永久不可见窗口。
		Hidden:            false,
		IgnoreMouseEvents: config.IgnoreMouseEvents,
		Windows: application.WindowsWindow{
			// 宠物是桌面浮层，不应额外占用任务栏入口；无边框装饰也不能替代真正的无框配置。
			HiddenOnTaskbar:                   true,
			DisableFramelessWindowDecorations: true,
		},
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
	// Hidden=true 让位置、尺寸和点击穿透先在窗口显示前确定；Wails 没有 showInactive，
	// 因此普通模式只能调用真实的 Show，keyboard 模式再显式 Focus。
	window.Show()
	// 指针观测与点击穿透是两条不同职责：即使窗口切到 interactive，仍需持续采样
	// 屏幕坐标，才能在鼠标离开宠物/菜单后让 renderer 恢复 passive 穿透。
	d.startPointerTracking(version, window)
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
	d.stopPointerTrackingLocked()
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
		// pointer tracking 随窗口生命周期存在，不随 IgnoreMouseEvents 开关停止。
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
	d.mu.Lock()
	window := d.window
	restore := d.focusRestore
	d.mu.Unlock()
	if window == nil {
		return nil
	}
	petHWND := windows.HWND(uintptr(window.NativeWindow()))
	if petHWND == 0 || windows.GetForegroundWindow() != petHWND {
		d.clearFocusRestore()
		return nil
	}

	// SetFocus 只作用于当前线程，SetForegroundWindow 才负责把键盘前台交还给旧窗口；
	// 通过 Wails 主线程执行原生调用，避免跨线程操作 WebView 所在线程的焦点。
	var foreground windows.HWND
	application.InvokeSync(func() {
		setFocusProc.Call(0)
		if restore != 0 {
			if valid, _, _ := isWindowProc.Call(uintptr(restore)); valid != 0 {
				setForegroundProc.Call(uintptr(restore))
			}
		}
		foreground = windows.GetForegroundWindow()
	})
	if foreground == petHWND {
		return fmt.Errorf("foreground window remained the pet window")
	}
	d.clearFocusRestore()
	return nil
}

func (d *wailsPetWindowDriver) clearFocusRestore() {
	d.mu.Lock()
	d.focusRestore = 0
	d.mu.Unlock()
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
	d.stopPointerTrackingLocked()
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

func (d *wailsPetWindowDriver) startPointerTracking(version uint64, window application.Window) {
	d.mu.Lock()
	if d.window != window || d.windowVersion != version || d.pointerStop != nil {
		d.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	d.pointerStop = stop
	d.mu.Unlock()

	go func() {
		ticker := time.NewTicker(30 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				d.emitPointerLocation(version, window)
			}
		}
	}()
}

func (d *wailsPetWindowDriver) stopPointerTrackingLocked() {
	if d.pointerStop == nil {
		return
	}
	close(d.pointerStop)
	d.pointerStop = nil
}

func (d *wailsPetWindowDriver) emitPointerLocation(version uint64, window application.Window) {
	d.mu.Lock()
	currentWindow := d.window
	currentVersion := d.windowVersion
	d.mu.Unlock()
	if currentWindow != window || currentVersion != version {
		return
	}

	hwnd := uintptr(window.NativeWindow())
	if hwnd == 0 {
		return
	}
	var cursor petWindowPoint
	var bounds petWindowRect
	if result, _, _ := getCursorPosProc.Call(uintptr(unsafe.Pointer(&cursor))); result == 0 {
		return
	}
	if result, _, _ := getWindowRectProc.Call(hwnd, uintptr(unsafe.Pointer(&bounds))); result == 0 {
		return
	}

	inside := cursor.x >= bounds.left && cursor.x < bounds.right && cursor.y >= bounds.top && cursor.y < bounds.bottom
	window.EmitEvent(petWindowPointerEvent, map[string]any{
		"screenX":      cursor.x,
		"screenY":      cursor.y,
		"windowX":      bounds.left,
		"windowY":      bounds.top,
		"windowWidth":  bounds.right - bounds.left,
		"windowHeight": bounds.bottom - bounds.top,
		"inside":       inside,
	})
}
