//go:build windows

package services

import (
	"fmt"
	"log"
	"strconv"
	"strings"
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
	getWindowLongProc    = user32PetWindow.NewProc("GetWindowLongW")
	setWindowLongProc    = user32PetWindow.NewProc("SetWindowLongW")
	setWindowPosProc     = user32PetWindow.NewProc("SetWindowPos")
	showWindowAsyncProc  = user32PetWindow.NewProc("ShowWindowAsync")
	isWindowProc         = user32PetWindow.NewProc("IsWindow")
	setFocusProc         = user32PetWindow.NewProc("SetFocus")
	setForegroundProc    = user32PetWindow.NewProc("SetForegroundWindow")
	getWindowProc        = user32PetWindow.NewProc("GetWindow")
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

// petWindowPointerSample 用于过滤没有任何几何变化的探测结果；Wails 没有
// Electron 的 forward mouse，原生层仍需探测穿透窗口的入/出，但静止光标不应
// 每次 ticker 都跨进程通知 renderer。
type petWindowPointerSample struct {
	screenX      int32
	screenY      int32
	windowX      int32
	windowY      int32
	windowWidth  int32
	windowHeight int32
	inside       bool
}

const petWindowPointerEvent = "pet.window.pointer"

const (
	petWindowHWNDTopMost      = ^uintptr(0)
	petWindowHWNDNoTopMost    = ^uintptr(1)
	petWindowHWNDTop          = uintptr(0)
	petWindowSWPNoSize        = 0x0001
	petWindowSWPNoMove        = 0x0002
	petWindowSWPNoZOrder      = 0x0004
	petWindowSWPNoActivate    = 0x0010
	petWindowSWPFrameChanged  = 0x0020
	petWindowSWPNoOwnerZOrder = 0x0200
	petWindowSWPShowWindow    = 0x0040
	petWindowSWShowNoActivate = 4
	petWindowGWLStyle         = -16
	petWindowGWLExStyle       = -20
	petWindowWSOverlapped     = 0x00cf0000
	petWindowWSPopup          = 0x80000000
	petWindowWSExNoActivate   = 0x08000000
	petWindowWSExTopmost      = 0x00000008
	petWindowWMMouseActivate  = 0x0021
	petWindowMANoActivate     = 3
	petWindowGWPrev           = 3
	// 只用于发现透明窗口的 hover 进入/离开；事件本身只在坐标或窗口边界变化时发出。
	// 50ms 对人眼交互已经足够，避免把 Wails bridge 当成 60Hz 鼠标转发总线。
	petWindowPointerInterval = 50 * time.Millisecond
)

func petWindowStyleIndex() uintptr {
	// Win32 的 GWL_STYLE 是有符号索引；先经过 int32 变量再转 uintptr，
	// 才能保留 -16 的底层位模式，同时避开 Go 对负常量直接转 uintptr 的编译期限制。
	index := int32(petWindowGWLStyle)
	return uintptr(index)
}

func petWindowExStyleIndex() uintptr {
	// GWL_EXSTYLE 同样是负的 Win32 索引；不能把负常量直接转换成 uintptr。
	index := int32(petWindowGWLExStyle)
	return uintptr(index)
}

// PetWindowWndProcInterceptor 阻止桌宠 overlay 因鼠标命中而激活顶层窗口。
// Wails alpha.38 没有处理 WM_MOUSEACTIVATE，默认处理可能返回 MA_ACTIVATE；
// 这会让底部全宽 interactive overlay 短暂成为前台，再触发 Wails 的 WM_SETFOCUS
// -> SetForegroundWindow 链路。只对带 WS_EX_NOACTIVATE 的窗口拦截，主窗口和普通
// Wails 窗口不会进入这条分支；keyboard 模式仍可通过显式 Window.Focus() 激活。
func PetWindowWndProcInterceptor(hwnd uintptr, msg uint32, wParam, lParam uintptr) (uintptr, bool) {
	if hwnd == 0 || msg != petWindowWMMouseActivate {
		return 0, false
	}

	exStyle, _, _ := getWindowLongProc.Call(hwnd, petWindowExStyleIndex())
	if uint32(exStyle)&uint32(petWindowWSExNoActivate) == 0 {
		return 0, false
	}

	WriteRuntimeDiagnostic("pet-native-mouse-activate-blocked", fmt.Sprintf(
		"hwnd=%#x wparam=%#x lparam=%#x result=%d",
		hwnd,
		wParam,
		lParam,
		petWindowMANoActivate,
	))
	return petWindowMANoActivate, true
}

func petWindowEnsureNoActivate(hwnd windows.HWND) error {
	if hwnd == 0 {
		return fmt.Errorf("pet window native handle is unavailable")
	}

	currentStyle, _, _ := getWindowLongProc.Call(uintptr(hwnd), petWindowExStyleIndex())
	if currentStyle == 0 {
		return petWindowLastError("GetWindowLongW exstyle")
	}
	desiredStyle := uint32(currentStyle) | uint32(petWindowWSExNoActivate)
	if desiredStyle != uint32(currentStyle) {
		setWindowLongProc.Call(
			uintptr(hwnd),
			petWindowExStyleIndex(),
			uintptr(desiredStyle),
		)
		result, _, _ := setWindowPosProc.Call(
			uintptr(hwnd),
			petWindowHWNDTop,
			0,
			0,
			0,
			0,
			uintptr(petWindowSWPNoMove|petWindowSWPNoSize|petWindowSWPNoZOrder|petWindowSWPNoOwnerZOrder|petWindowSWPNoActivate|petWindowSWPFrameChanged),
		)
		if result == 0 {
			return petWindowLastError("SetWindowPos noactivate")
		}
	}

	updatedStyle, _, _ := getWindowLongProc.Call(uintptr(hwnd), petWindowExStyleIndex())
	if uint32(updatedStyle)&uint32(petWindowWSExNoActivate) == 0 {
		return fmt.Errorf("SetWindowLongW exstyle did not apply WS_EX_NOACTIVATE")
	}
	return nil
}

func petWindowHWND(window application.Window) (windows.HWND, error) {
	if window == nil {
		return 0, fmt.Errorf("pet window is nil")
	}
	hwnd := windows.HWND(uintptr(window.NativeWindow()))
	if hwnd == 0 {
		return 0, fmt.Errorf("pet window native handle is unavailable")
	}
	valid, _, callErr := isWindowProc.Call(uintptr(hwnd))
	if valid == 0 {
		if callErr != windows.ERROR_SUCCESS {
			return 0, fmt.Errorf("IsWindow(%#x): %w", hwnd, callErr)
		}
		return 0, fmt.Errorf("IsWindow(%#x) returned false", hwnd)
	}
	return hwnd, nil
}

func petWindowLastError(operation string) error {
	err := windows.GetLastError()
	if err == windows.ERROR_SUCCESS {
		return fmt.Errorf("%s failed", operation)
	}
	return fmt.Errorf("%s failed: %w", operation, err)
}

func petWindowMakeFramelessWithoutActivation(hwnd windows.HWND) error {
	if hwnd == 0 {
		return fmt.Errorf("pet window native handle is unavailable")
	}

	// Wails alpha.38 的 Frameless 初始化会用不带 SWP_NOACTIVATE 的
	// SetWindowPos 触发一次前台切换。先按隐藏普通窗口创建，再在窗口尚未显示时
	// 自己切换为 WS_POPUP，避免把用户当前正在使用的窗口短暂顶到后台。
	style, _, _ := getWindowLongProc.Call(uintptr(hwnd), petWindowStyleIndex())
	if style == 0 {
		return petWindowLastError("GetWindowLongW style")
	}
	desiredStyle := (uint32(style) &^ uint32(petWindowWSOverlapped)) | uint32(petWindowWSPopup)
	setWindowLongProc.Call(
		uintptr(hwnd),
		petWindowStyleIndex(),
		uintptr(desiredStyle),
	)
	// HiddenOnTaskbar 会在 Wails 创建时附带 WS_EX_NOACTIVATE，但这是 Wails 的
	// 选项副作用，不是宠物窗口自己的不变量；创建完成后显式补齐，避免后续
	// Chromium/Wails 样式更新把“不可由鼠标激活”依赖变成隐式行为。
	if err := petWindowEnsureNoActivate(hwnd); err != nil {
		return err
	}

	flags := uintptr(
		petWindowSWPNoMove |
			petWindowSWPNoSize |
			petWindowSWPNoZOrder |
			petWindowSWPNoOwnerZOrder |
			petWindowSWPNoActivate |
			petWindowSWPFrameChanged,
	)
	result, _, _ := setWindowPosProc.Call(uintptr(hwnd), 0, 0, 0, 0, 0, flags)
	if result == 0 {
		return petWindowLastError("SetWindowPos frameless")
	}

	updatedStyle, _, _ := getWindowLongProc.Call(uintptr(hwnd), petWindowStyleIndex())
	if uint32(updatedStyle)&uint32(petWindowWSPopup) == 0 {
		return fmt.Errorf("SetWindowLongW style did not apply WS_POPUP")
	}
	return nil
}

func readPetWindowRect(hwnd windows.HWND) (petWindowRect, error) {
	var rect petWindowRect
	result, _, _ := getWindowRectProc.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect)))
	if result == 0 {
		return petWindowRect{}, petWindowLastError("GetWindowRect")
	}
	return rect, nil
}

func petWindowSetNativePosition(hwnd windows.HWND, x, y int, width, height int, topMost bool, show bool) error {
	// 非置顶不是“沉到所有普通窗口后面”：桌宠需要先在普通窗口 Z 序里可见，
	// 之后用户激活其他应用时它才会自然退到后面。若使用 HWND_NOTOPMOST，
	// 主窗口仍在前台时透明桌宠会完整落在主窗口后面，表现为窗口存在但宠物消失。
	insertAfter := petWindowHWNDTop
	if topMost {
		insertAfter = petWindowHWNDTopMost
	}
	flags := uintptr(petWindowSWPNoActivate)
	if width <= 0 || height <= 0 {
		flags |= petWindowSWPNoSize
	}
	if x == 0 && y == 0 && width <= 0 && height <= 0 {
		flags |= petWindowSWPNoMove
	}
	if show {
		flags |= petWindowSWPShowWindow
	}
	result, _, _ := setWindowPosProc.Call(
		uintptr(hwnd),
		insertAfter,
		uintptr(int32(x)),
		uintptr(int32(y)),
		uintptr(width),
		uintptr(height),
		flags,
	)
	if result == 0 {
		return petWindowLastError("SetWindowPos")
	}
	return nil
}

func petWindowSetNativeTopmost(hwnd windows.HWND, topMost bool) error {
	insertAfter := petWindowHWNDNoTopMost
	if topMost {
		insertAfter = petWindowHWNDTopMost
	}
	flags := uintptr(petWindowSWPNoActivate | petWindowSWPNoSize | petWindowSWPNoMove)
	result, _, _ := setWindowPosProc.Call(
		uintptr(hwnd),
		insertAfter,
		0,
		0,
		0,
		0,
		flags,
	)
	if result == 0 {
		return petWindowLastError("SetWindowPos topmost")
	}
	return nil
}

func petWindowParsePlatformHWND(platformID string) (windows.HWND, error) {
	platformID = strings.TrimSpace(platformID)
	if platformID == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(platformID, 0, strconv.IntSize)
	if err != nil || value == 0 {
		if err == nil {
			err = fmt.Errorf("empty or zero window handle")
		}
		return 0, fmt.Errorf("invalid platform window id %q: %w", platformID, err)
	}
	hwnd := windows.HWND(value)
	valid, _, callErr := isWindowProc.Call(uintptr(hwnd))
	if valid == 0 {
		if callErr != windows.ERROR_SUCCESS {
			return 0, fmt.Errorf("platform window %q is invalid: %w", platformID, callErr)
		}
		return 0, fmt.Errorf("platform window %q is not a window", platformID)
	}
	return hwnd, nil
}

func petWindowIsTopmost(hwnd windows.HWND) bool {
	if hwnd == 0 {
		return false
	}
	exStyle, _, _ := getWindowLongProc.Call(uintptr(hwnd), petWindowExStyleIndex())
	return uint32(exStyle)&uint32(petWindowWSExTopmost) != 0
}

func petWindowZOrderPredecessor(hwnd windows.HWND) windows.HWND {
	if hwnd == 0 {
		return 0
	}
	previous, _, _ := getWindowProc.Call(uintptr(hwnd), uintptr(petWindowGWPrev))
	return windows.HWND(previous)
}

func petWindowSetNativePlatformLayer(hwnd, platform windows.HWND) (bool, error) {
	if hwnd == 0 {
		return false, fmt.Errorf("pet window native handle is unavailable")
	}
	if platform == hwnd {
		return false, fmt.Errorf("platform window cannot be the pet window itself")
	}
	if platform == 0 {
		if err := petWindowSetNativeTopmost(hwnd, false); err != nil {
			return false, err
		}
		if err := petWindowSetNativePosition(hwnd, 0, 0, 0, 0, false, false); err != nil {
			return false, err
		}
		return false, nil
	}
	if !windows.IsWindowVisible(platform) {
		return false, fmt.Errorf("platform window %#x is not visible", uintptr(platform))
	}

	topMost := petWindowIsTopmost(platform)
	previous := petWindowZOrderPredecessor(platform)
	if previous == hwnd && petWindowIsTopmost(hwnd) == topMost {
		// 桌宠已经紧邻目标窗口上方；仍需确保它处于目标窗口所属的 topmost
		// 分组，层级和位置都已正确，不能再次 SetWindowPos 造成 Z 序抖动。
		return topMost, nil
	}
	if err := petWindowSetNativeTopmost(hwnd, topMost); err != nil {
		return false, err
	}

	// 切换 topmost 分组本身会改变桌宠的 Z 序；必须重新读取目标窗口的前置
	// 窗口，不能复用切换前的 predecessor，否则桌宠可能被插到错误的分组。
	insertAfter := petWindowZOrderPredecessor(platform)
	if insertAfter == hwnd {
		return topMost, nil
	}
	if insertAfter == 0 || petWindowIsTopmost(insertAfter) != topMost {
		// 同一 Z 序分组没有可直接插入的前置窗口时，放到该分组最前面；
		// 它仍只高于目标窗口所属层级，不会伪装成永久 topmost。
		if topMost {
			insertAfter = windows.HWND(petWindowHWNDTopMost)
		} else {
			insertAfter = windows.HWND(petWindowHWNDTop)
		}
	}
	flags := uintptr(petWindowSWPNoActivate | petWindowSWPNoSize | petWindowSWPNoMove | petWindowSWPNoOwnerZOrder)
	result, _, _ := setWindowPosProc.Call(
		uintptr(hwnd),
		uintptr(insertAfter),
		0,
		0,
		0,
		0,
		flags,
	)
	if result == 0 {
		return false, petWindowLastError("SetWindowPos platform layer")
	}
	return topMost, nil
}

func showPetWindowWithoutActivation(window application.Window, topMost bool) error {
	if window == nil {
		return ErrPetWindowNotReady
	}
	WriteRuntimeDiagnostic("pet-native-show-start", fmt.Sprintf("topmost=%t foreground_before=%#x", topMost, uintptr(windows.GetForegroundWindow())))

	// 复用路径可能还没有进入 Wails 主线程；Run 只负责确保 HWND 已创建，不能
	// 代替显示。真正显示必须等 WebView 首次导航完成，并且全部走 Win32 非激活 API，
	// 否则调用 Wails Show() 仍会执行 SW_SHOW，启动时就可能抢走 Terminal 前台。
	window.Run()

	// Wails 的窗口 API 本身会把原生操作调度到主线程；这里的 Win32 调用也沿用
	// 同一线程边界，避免导航事件 goroutine 直接改 WebView 所属 HWND 的 Z-order。
	return application.InvokeSyncWithError(func() error {
		hwnd, err := petWindowHWND(window)
		if err != nil {
			WriteRuntimeDiagnostic("pet-native-show-hwnd-failed", fmt.Sprintf("err=%q", err.Error()))
			return err
		}
		WriteRuntimeDiagnostic("pet-native-show-hwnd", fmt.Sprintf("hwnd=%#x topmost=%t", uintptr(hwnd), topMost))
		if err := petWindowEnsureNoActivate(hwnd); err != nil {
			WriteRuntimeDiagnostic("pet-native-show-noactivate-failed", fmt.Sprintf("hwnd=%#x err=%q", uintptr(hwnd), err.Error()))
			return err
		}
		if err := petWindowSetNativeTopmost(hwnd, topMost); err != nil {
			WriteRuntimeDiagnostic("pet-native-show-topmost-failed", fmt.Sprintf("hwnd=%#x err=%q", uintptr(hwnd), err.Error()))
			return err
		}
		if err := petWindowSetNativePosition(hwnd, 0, 0, 0, 0, topMost, true); err != nil {
			WriteRuntimeDiagnostic("pet-native-show-position-failed", fmt.Sprintf("hwnd=%#x err=%q", uintptr(hwnd), err.Error()))
			return err
		}

		// SetWindowPos 已经用 SWP_NOACTIVATE 显示窗口；再发
		// SW_SHOWNOACTIVATE 是为了覆盖窗口此前处于隐藏/最小化状态的情况，且
		// ShowWindowAsync 不会把当前前台窗口交给桌宠。
		showWindowAsyncProc.Call(uintptr(hwnd), uintptr(petWindowSWShowNoActivate))
		if !windows.IsWindowVisible(hwnd) {
			return fmt.Errorf("show pet window without activation: native window is not visible")
		}
		WriteRuntimeDiagnostic("pet-native-show-success", fmt.Sprintf("hwnd=%#x foreground_after=%#x", uintptr(hwnd), uintptr(windows.GetForegroundWindow())))
		return nil
	})
}

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
	pointerSample  petWindowPointerSample
	focusRestore   windows.HWND
	alwaysOnTop    bool
	windowShowing  bool
	windowShown    bool
	pendingFocus   bool
	platforms      *petWindowPlatformMonitor
}

func newPetWindowDriver(app *application.App, options PetWindowOptions) (petWindowDriver, error) {
	if app == nil || app.Window == nil {
		return nil, ErrPetWindowNilApplication
	}
	return &wailsPetWindowDriver{
		app:       app,
		platforms: newPetWindowPlatformMonitor(app),
	}, nil
}

func (d *wailsPetWindowDriver) GetPlatforms() (PetWindowPlatformSnapshot, error) {
	d.mu.Lock()
	monitor := d.platforms
	shown := d.windowShown
	d.mu.Unlock()
	if monitor == nil || !shown {
		return PetWindowPlatformSnapshot{}, nil
	}
	return monitor.GetPlatforms()
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
		version := d.windowVersion
		if !d.windowShown && !d.windowShowing {
			d.alwaysOnTop = config.AlwaysOnTop
			d.mu.Unlock()
			// 首次导航尚未完成时，窗口已经由 Wails 创建但仍保持隐藏；重复 Open
			// 只能更新待显示策略，不能提前走显示 API 破坏非激活启动保证。
			return nil
		}
		// Wails 在窗口销毁后会让 NativeWindow 返回 nil，但 driver 自身的引用
		// 不一定已经先于下一次 Open 清掉；失效引用不能进入复用路径，否则后续
		// setter 只会作用于一个已经没有原生宿主的半残对象。
		if _, hwndErr := petWindowHWND(window); hwndErr != nil {
			d.detachPetWindowLocked(window, version)
		} else {
			d.mu.Unlock()
			// 逻辑窗口可能仍然存在，但 WorkArea、DPI 或用户之前的手动调整
			// 已经让原生几何过期；复用时必须重新应用，而不是直接 early return。
			if err := applyPetWindowOpenConfig(window, config); err != nil {
				d.cleanupFailedPetWindow(window, version)
				return fmt.Errorf("reapply pet window config: %w", err)
			}
			if err := showPetWindowWithoutActivation(window, config.AlwaysOnTop); err != nil {
				d.cleanupFailedPetWindow(window, version)
				return fmt.Errorf("reapply pet window without activation: %w", err)
			}
			d.mu.Lock()
			if d.window == window && d.windowVersion == version {
				d.alwaysOnTop = config.AlwaysOnTop
				d.windowShown = true
				d.windowShowing = false
			}
			d.mu.Unlock()
			// Close() 只停止轮询，窗口复用时必须把轮询重新接上；否则第一次关闭/再开
			// 后 renderer 再也收不到屏幕坐标，透明窗口会卡在上一次交互模式。
			d.startPointerTracking(version, window)
			d.startPlatformMonitor(version, window)
			return nil
		}
	}

	windowOptions := application.WebviewWindowOptions{
		Name:          config.Name,
		Title:         config.Title,
		Width:         config.Width,
		Height:        config.Height,
		AlwaysOnTop:   config.AlwaysOnTop,
		URL:           config.URL,
		DisableResize: true,
		// 不把 Frameless 交给 Wails alpha.38；它的初始化会用无 SWP_NOACTIVATE 的
		// SetWindowPos 抢一次前台。窗口创建后由 petWindowMakeFramelessWithoutActivation
		// 在隐藏状态下完成同等的 WS_POPUP 切换。
		Frameless:        false,
		BackgroundType:   application.BackgroundTypeTransparent,
		BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		// 先隐藏创建，避免 Wails 在 app.Run 或 WebView 初始化阶段用 SW_SHOW
		// 把桌宠带入前台；显示由 showPetWindowWithoutActivation 统一接管。
		Hidden:            true,
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

	foregroundBeforeCreate := windows.GetForegroundWindow()
	WriteRuntimeDiagnostic("pet-native-create-start", fmt.Sprintf("foreground_before=%#x", uintptr(foregroundBeforeCreate)))
	window := d.app.Window.NewWithOptions(windowOptions)
	petHWND := windows.HWND(uintptr(window.NativeWindow()))
	if err := application.InvokeSyncWithError(func() error {
		return petWindowMakeFramelessWithoutActivation(petHWND)
	}); err != nil {
		d.mu.Unlock()
		window.Close()
		window.Hide()
		return fmt.Errorf("make pet window frameless without activation: %w", err)
	}
	foregroundAfterCreate := windows.GetForegroundWindow()
	WriteRuntimeDiagnostic("pet-native-create-completed", fmt.Sprintf(
		"hwnd=%#x foreground_after=%#x changed=%t",
		uintptr(petHWND),
		uintptr(foregroundAfterCreate),
		foregroundBeforeCreate != foregroundAfterCreate,
	))
	// Wails/Chromium 初始化偶尔会改变前台窗口，但这里不能再用
	// SetForegroundWindow 把任意外部窗口强行拉回来；这种恢复动作会放大外部窗口
	// 的前台抖动风险。当前 WT 闪动尚未证实走到此路径，日志只能作为后续归因依据。
	// 桌宠创建和显示均已使用隐藏/非激活路径，初始化不应主动改变用户焦点。
	d.windowVersion++
	version := d.windowVersion
	window.RegisterHook(events.Common.WindowClosing, func(_ *application.WindowEvent) {
		d.handleWindowClosing(version)
	})
	window.RegisterHook(events.Windows.WebViewNavigationCompleted, func(_ *application.WindowEvent) {
		d.handleWindowNavigationCompleted(version, window)
	})
	d.window = window
	d.alwaysOnTop = config.AlwaysOnTop
	d.windowShowing = false
	d.windowShown = false
	d.pendingFocus = false
	d.mu.Unlock()
	// NewWithOptions 在应用运行后会同步完成 HWND/WebView 创建，但桌宠仍保持 Hidden；
	// 首次显示由 WebViewNavigationCompleted hook 驱动，避免在 Chromium 初始化窗口阶段
	// 触发 Wails 的 WM_SETFOCUS -> SetForegroundWindow 链路。
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
	monitor := d.platforms
	d.mu.Unlock()
	if monitor != nil {
		monitor.Stop()
	}
	window.Close()
	// Wails alpha.38 的 Close() 没有错误返回值，而且 WindowClosing 事件通过
	// 事件队列异步分发；不能用“调用返回时 hook 尚未执行”伪造关闭失败。
	// 先解除 driver 所有权，避免下一次 Open 复用旧对象；若底层关闭事件被延迟，
	// Wails 仍会继续处理已经发出的关闭请求。
	window.Hide()
	d.mu.Lock()
	d.detachPetWindowLocked(window, d.windowVersion)
	d.mu.Unlock()
	return nil
}

// detachPetWindowLocked 只清理 driver 的生命周期状态，不触碰 native window。
// 调用方必须已经持有 d.mu；保留这个边界是为了让异步 WindowClosing hook 和
// 失败路径使用同一套版本校验，避免旧窗口的回调误清理新窗口。
func (d *wailsPetWindowDriver) detachPetWindowLocked(window application.Window, version uint64) {
	if d.window != window || d.windowVersion != version {
		return
	}
	d.window = nil
	d.stopPointerTrackingLocked()
	d.windowShowing = false
	d.windowShown = false
	d.pendingFocus = false
	d.alwaysOnTop = false
	if d.closingVersion == version {
		d.closingVersion = 0
	}
}

// cleanupFailedPetWindow 用于 Open 的中途失败。Wails 的 Close 无法返回底层
// 错误，因此这里同时发出关闭请求并 Hide 兜底，先确保失败的透明 overlay 不会
// 留在屏幕上，再丢弃 driver 引用；这比把一个几何未完成的窗口留给复用更安全。
func (d *wailsPetWindowDriver) cleanupFailedPetWindow(window application.Window, version uint64) {
	d.mu.Lock()
	if d.window != window || d.windowVersion != version {
		d.mu.Unlock()
		return
	}
	d.closingVersion = version
	d.stopPointerTrackingLocked()
	monitor := d.platforms
	d.mu.Unlock()
	if monitor != nil {
		monitor.Stop()
	}

	window.Close()
	window.Hide()

	d.mu.Lock()
	d.detachPetWindowLocked(window, version)
	d.mu.Unlock()
}

func (d *wailsPetWindowDriver) SetIgnoreMouseEvents(ignore bool) error {
	d.mu.Lock()
	window := d.window
	d.mu.Unlock()
	if window != nil {
		foregroundBefore := windows.GetForegroundWindow()
		window.SetIgnoreMouseEvents(ignore)
		// SetIgnoreMouseEvents 只改透明鼠标样式，但它是 interactive/passive 的
		// 唯一切换点；每次切换后重新确认 no-activate，防止底层窗口样式被第三方
		// WebView 逻辑改写后，下一次点击又回到默认激活行为。
		if err := application.InvokeSyncWithError(func() error {
			hwnd, err := petWindowHWND(window)
			if err != nil {
				return err
			}
			return petWindowEnsureNoActivate(hwnd)
		}); err != nil {
			return fmt.Errorf("ensure pet window noactivate: %w", err)
		}
		// Wails alpha.38 不返回底层错误，只能通过公开状态 getter 验证结果。
		actual := window.IsIgnoreMouseEvents()
		foregroundAfter := windows.GetForegroundWindow()
		WriteRuntimeDiagnostic("pet-native-mouse-mode", fmt.Sprintf(
			"ignore=%t actual=%t foreground_before=%#x foreground_after=%#x",
			ignore,
			actual,
			uintptr(foregroundBefore),
			uintptr(foregroundAfter),
		))
		if actual != ignore {
			return fmt.Errorf("set ignore mouse events: requested %t, got %t", ignore, actual)
		}
		// pointer tracking 随窗口生命周期存在，不随 IgnoreMouseEvents 开关停止。
	}
	return nil
}

func (d *wailsPetWindowDriver) Focus() error {
	d.mu.Lock()
	window := d.window
	if window != nil && !d.windowShown {
		// Open 在导航完成前是异步可见的；键盘模式仍需保留用户意图，等非激活
		// 显示完成后再执行真实 Focus，不能为了规避启动抢焦点而直接丢掉输入能力。
		d.pendingFocus = true
		d.mu.Unlock()
		return nil
	}
	d.mu.Unlock()
	if window == nil {
		return nil
	}
	// alpha.38 的 Focus() 直接调用内部实现，窗口尚未进入 Wails 主线程时会触发 nil 解引用；
	// 先用公共接口确认窗口已显示，宁可返回可识别错误，也不伪造“已聚焦”。
	if !window.IsVisible() {
		return ErrPetWindowNotReady
	}
	foregroundBefore := windows.GetForegroundWindow()
	window.Focus()
	WriteRuntimeDiagnostic("pet-native-focus", fmt.Sprintf(
		"hwnd=%#x foreground_before=%#x foreground_after=%#x",
		uintptr(window.NativeWindow()),
		uintptr(foregroundBefore),
		uintptr(windows.GetForegroundWindow()),
	))
	return nil
}

func (d *wailsPetWindowDriver) ReleaseFocus() error {
	d.mu.Lock()
	window := d.window
	restore := d.focusRestore
	shown := d.windowShown
	d.pendingFocus = false
	d.mu.Unlock()
	if window == nil || !shown {
		d.clearFocusRestore()
		return nil
	}
	petHWND := windows.HWND(uintptr(window.NativeWindow()))
	if petHWND == 0 || windows.GetForegroundWindow() != petHWND {
		d.clearFocusRestore()
		return nil
	}
	foregroundBefore := windows.GetForegroundWindow()

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
	WriteRuntimeDiagnostic("pet-native-release-focus", fmt.Sprintf(
		"hwnd=%#x restore=%#x foreground_before=%#x foreground_after=%#x",
		uintptr(petHWND),
		uintptr(restore),
		uintptr(foregroundBefore),
		uintptr(foreground),
	))
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
		// Wails 的窗口 API 使用 DIP；GetWindowRect 返回物理像素，不能直接比较。
		// 通过同一套 Wails getter 回读，既覆盖高 DPI，也覆盖负坐标副屏。
		actualX, actualY := window.Position()
		if actualX != x || actualY != y {
			return fmt.Errorf("set pet window position: requested (%d, %d), got (%d, %d)", x, y, actualX, actualY)
		}
	}
	return nil
}

func (d *wailsPetWindowDriver) SetSize(width, height int) error {
	d.mu.Lock()
	window := d.window
	d.mu.Unlock()
	if window != nil {
		window.SetSize(width, height)
		// 与 SetPosition 一样，Size() 返回的是 Wails 对外承诺的 DIP 尺寸。
		actualWidth, actualHeight := window.Size()
		if actualWidth != width || actualHeight != height {
			return fmt.Errorf("set pet window size: requested (%d, %d), got (%d, %d)", width, height, actualWidth, actualHeight)
		}
	}
	return nil
}

func (d *wailsPetWindowDriver) SetAlwaysOnTop(alwaysOnTop bool) error {
	d.mu.Lock()
	window := d.window
	d.alwaysOnTop = alwaysOnTop
	d.mu.Unlock()
	if window != nil {
		hwnd, err := petWindowHWND(window)
		if err != nil {
			return fmt.Errorf("set pet window always-on-top: %w", err)
		}
		if err := petWindowEnsureNoActivate(hwnd); err != nil {
			return fmt.Errorf("set pet window noactivate: %w", err)
		}
		foregroundBefore := windows.GetForegroundWindow()
		// Wails 的 SetAlwaysOnTop 内部没有 SWP_NOACTIVATE，会在桌宠设置切换
		// 置顶时再次争抢前台；Windows 驱动直接收敛到原生非激活 Z-order。
		if err := petWindowSetNativeTopmost(hwnd, alwaysOnTop); err != nil {
			return fmt.Errorf("set pet window always-on-top: %w", err)
		}
		if err := petWindowSetNativePosition(hwnd, 0, 0, 0, 0, alwaysOnTop, false); err != nil {
			return fmt.Errorf("set pet window always-on-top: %w", err)
		}
		WriteRuntimeDiagnostic("pet-native-topmost", fmt.Sprintf(
			"hwnd=%#x topmost=%t foreground_before=%#x foreground_after=%#x",
			uintptr(hwnd),
			alwaysOnTop,
			uintptr(foregroundBefore),
			uintptr(windows.GetForegroundWindow()),
		))
	}
	return nil
}

func (d *wailsPetWindowDriver) SetPlatformLayer(platformID string) (bool, error) {
	d.mu.Lock()
	window := d.window
	d.mu.Unlock()
	if window == nil {
		return false, nil
	}

	topMost := false
	err := application.InvokeSyncWithError(func() error {
		hwnd, err := petWindowHWND(window)
		if err != nil {
			return err
		}
		platform, err := petWindowParsePlatformHWND(platformID)
		if err != nil {
			return err
		}
		topMost, err = petWindowSetNativePlatformLayer(hwnd, platform)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("set pet window platform layer: %w", err)
	}

	d.mu.Lock()
	d.alwaysOnTop = topMost
	d.mu.Unlock()
	return topMost, nil
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
	d.windowShowing = false
	d.windowShown = false
	d.pendingFocus = false
	wasExplicitClose := d.closingVersion == version
	if wasExplicitClose {
		d.closingVersion = 0
	}
	callback := d.windowClosedFn
	monitor := d.platforms
	d.mu.Unlock()
	if monitor != nil {
		monitor.Stop()
	}
	if !wasExplicitClose && callback != nil {
		callback()
	}
}

// handleWindowNavigationCompleted 在 Wails 已完成首次 WebView 导航后才显示桌宠。
// Wails 自身的导航回调会在隐藏父窗口内完成 Chromium Hide/Show；此处再用非激活
// Win32 API 显示父窗口，切断“初始化期间焦点 -> 前台窗口”的竞态。
func (d *wailsPetWindowDriver) handleWindowNavigationCompleted(version uint64, window application.Window) {
	WriteRuntimeDiagnostic("pet-navigation-completed", fmt.Sprintf("version=%d", version))
	d.mu.Lock()
	if d.window != window || d.windowVersion != version || d.windowShown || d.windowShowing {
		d.mu.Unlock()
		return
	}
	d.windowShowing = true
	d.mu.Unlock()

	d.mu.Lock()
	topMost := d.alwaysOnTop
	d.mu.Unlock()
	if err := showPetWindowWithoutActivation(window, topMost); err != nil {
		d.mu.Lock()
		if d.window == window && d.windowVersion == version {
			d.windowShowing = false
		}
		d.mu.Unlock()
		log.Printf("[PetWindow] 首次导航完成后显示桌宠失败: %v", err)
		return
	}
	WriteRuntimeDiagnostic("pet-navigation-show-completed", fmt.Sprintf("version=%d topmost=%t", version, topMost))

	d.mu.Lock()
	if d.window != window || d.windowVersion != version {
		d.mu.Unlock()
		return
	}
	d.windowShowing = false
	d.windowShown = true
	shouldFocus := d.pendingFocus
	d.pendingFocus = false
	d.mu.Unlock()

	// 指针观测与点击穿透是两条不同职责：只有窗口真正显示后才启动采样，
	// 避免初始化期间向尚未可见的 renderer 投递一串无效坐标事件。
	d.startPointerTracking(version, window)
	d.startPlatformMonitor(version, window)
	if shouldFocus {
		window.Focus()
	}
}

func (d *wailsPetWindowDriver) startPlatformMonitor(version uint64, window application.Window) {
	d.mu.Lock()
	if d.window != window || d.windowVersion != version || !d.windowShown || d.platforms == nil {
		d.mu.Unlock()
		return
	}
	monitor := d.platforms
	hwnd := windows.HWND(uintptr(window.NativeWindow()))
	d.mu.Unlock()

	if hwnd == 0 {
		return
	}
	err := application.InvokeSyncWithError(func() error {
		return monitor.Start(hwnd)
	})
	if err != nil {
		WriteRuntimeDiagnostic("pet-platform-monitor-start-failed", fmt.Sprintf("hwnd=%#x err=%q", uintptr(hwnd), err.Error()))
		log.Printf("[PetWindow] 启动窗口平台监视失败: %v", err)
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
	d.pointerSample = petWindowPointerSample{}
	d.mu.Unlock()

	go func() {
		ticker := time.NewTicker(petWindowPointerInterval)
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
	d.pointerSample = petWindowPointerSample{}
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
	sample := petWindowPointerSample{
		screenX:      cursor.x,
		screenY:      cursor.y,
		windowX:      bounds.left,
		windowY:      bounds.top,
		windowWidth:  bounds.right - bounds.left,
		windowHeight: bounds.bottom - bounds.top,
		inside:       inside,
	}

	d.mu.Lock()
	if d.window != window || d.windowVersion != version || d.pointerSample == sample {
		d.mu.Unlock()
		return
	}
	d.pointerSample = sample
	d.mu.Unlock()

	window.EmitEvent(petWindowPointerEvent, map[string]any{
		"screenX":      sample.screenX,
		"screenY":      sample.screenY,
		"windowX":      sample.windowX,
		"windowY":      sample.windowY,
		"windowWidth":  sample.windowWidth,
		"windowHeight": sample.windowHeight,
		"inside":       inside,
	})
}
