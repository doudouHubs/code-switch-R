package services

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	DefaultPetWindowName   = "pet-window"
	DefaultPetWindowTitle  = "Pet"
	DefaultPetWindowWidth  = 420
	DefaultPetWindowHeight = 380
)

var (
	ErrPetWindowInvalidMode       = errors.New("invalid pet window mode")
	ErrPetWindowInvalidSize       = errors.New("pet window size must be positive")
	ErrPetWindowNilDriver         = errors.New("pet window driver is nil")
	ErrPetWindowNilApplication    = errors.New("pet window requires a Wails application")
	ErrPetWindowNotReady          = errors.New("pet window is not ready for focus")
	ErrPetWindowScreenUnavailable = errors.New("pet window primary screen is unavailable")
)

// PetWindowOptions 描述窗口宿主自身的配置；URL 必须由主控注入，宿主不决定前端路由。
type PetWindowOptions struct {
	Name  string
	Title string
	URL   string

	Width  int
	Height int

	// PositionSet 为 true 时才使用 X/Y，允许调用方明确选择绝对屏幕坐标 (包括负坐标的副屏)。
	PositionSet bool
	X           int
	Y           int
}

type petWindowOpenConfig struct {
	Name  string
	Title string
	URL   string

	Width  int
	Height int

	PositionSet bool
	X           int
	Y           int

	AlwaysOnTop       bool
	IgnoreMouseEvents bool
}

// resolvePetWindowOverlayConfig 为每次创建桌宠窗口补齐原版 overlay 几何。
// PositionSet 只代表调用方是否提供了坐标，不能代表窗口已经是 overlay 尺寸；
// 宠物窗口的尺寸和底部位置必须始终由 work area 重新计算，否则窗口被 Move/Resize
// 过一次后再次打开会退回普通小窗口，前端再努力漫游也只能困在中间那一块。
func resolvePetWindowOverlayConfig(app *application.App, config petWindowOpenConfig) (petWindowOpenConfig, error) {
	if app == nil || app.Screen == nil {
		return petWindowOpenConfig{}, ErrPetWindowScreenUnavailable
	}

	primaryScreen := app.Screen.GetPrimary()
	if primaryScreen == nil || primaryScreen.WorkArea.Width <= 0 || primaryScreen.WorkArea.Height <= 0 {
		return petWindowOpenConfig{}, ErrPetWindowScreenUnavailable
	}

	workArea := primaryScreen.WorkArea
	height := DefaultPetWindowHeight
	if height > workArea.Height {
		height = workArea.Height
	}
	config.Width = workArea.Width
	config.Height = height
	config.PositionSet = true
	config.X = workArea.X
	config.Y = workArea.Y + workArea.Height - height
	return config, nil
}

// petWindowDriver 是公共状态机和 Wails API 之间的最小边界。
// Wails alpha.38 没有 Electron 的 setFocusable/blur/showInactive/forward，
// 因此这里故意只暴露 Wails 真实存在的操作，测试可以用 fake 替换平台实现。
type petWindowDriver interface {
	Open(config petWindowOpenConfig) error
	Close() error
	SetWindowClosedCallback(callback func())
	CaptureFocusRestore()
	SetIgnoreMouseEvents(ignore bool) error
	Focus() error
	ReleaseFocus() error
	SetPosition(x, y int) error
	SetSize(width, height int) error
	SetAlwaysOnTop(alwaysOnTop bool) error
	IsFocused() bool
}

func applyPetWindowOpenConfig(window application.Window, config petWindowOpenConfig) error {
	if window == nil {
		return ErrPetWindowNotReady
	}
	window.SetSize(config.Width, config.Height)
	if config.PositionSet {
		window.SetPosition(config.X, config.Y)
	}
	window.SetIgnoreMouseEvents(config.IgnoreMouseEvents)

	// Wails 的窗口 setter 不返回底层错误，回读尺寸/位置和点击模式，
	// 才能确认复用路径确实摆脱了旧的小窗口状态。窗口显示由平台 driver
	// 负责：Windows 的显示与 Z-order 都必须走 SWP_NOACTIVATE，不能在公共层
	// 调用可能激活窗口的 Show/SetAlwaysOnTop。
	actualWidth, actualHeight := window.Size()
	if actualWidth != config.Width || actualHeight != config.Height {
		return fmt.Errorf("reapply pet window size: requested %dx%d, got %dx%d", config.Width, config.Height, actualWidth, actualHeight)
	}
	if config.PositionSet {
		actualX, actualY := window.Position()
		if actualX != config.X || actualY != config.Y {
			return fmt.Errorf("reapply pet window position: requested (%d, %d), got (%d, %d)", config.X, config.Y, actualX, actualY)
		}
	}
	if actual := window.IsIgnoreMouseEvents(); actual != config.IgnoreMouseEvents {
		return fmt.Errorf("reapply pet window mouse mode: requested %t, got %t", config.IgnoreMouseEvents, actual)
	}
	return nil
}

// PetWindow 是宠物桌面窗口的线程安全宿主。它不负责注册服务，也不负责加载前端页面。
type PetWindow struct {
	mu     sync.Mutex
	driver petWindowDriver

	name  string
	title string
	url   string

	width  int
	height int

	positionSet bool
	x           int
	y           int

	mode        PetWindowMode
	open        bool
	alwaysOnTop bool
}

// NewPetWindow 创建一个由 Wails application 托管的宠物窗口。
// 窗口会在 Open 时才创建，便于主控先完成 app/service 初始化，也便于 Close 后再次 Open。
func NewPetWindow(app *application.App, options PetWindowOptions) (*PetWindow, error) {
	normalized, err := normalizePetWindowOptions(options)
	if err != nil {
		return nil, err
	}

	driver, err := newPetWindowDriver(app, normalized)
	if err != nil {
		return nil, err
	}
	return newPetWindowWithDriver(driver, normalized)
}

func newPetWindowWithDriver(driver petWindowDriver, options PetWindowOptions) (*PetWindow, error) {
	if driver == nil {
		return nil, ErrPetWindowNilDriver
	}

	normalized, err := normalizePetWindowOptions(options)
	if err != nil {
		return nil, err
	}

	window := &PetWindow{
		driver:      driver,
		name:        normalized.Name,
		title:       normalized.Title,
		url:         normalized.URL,
		width:       normalized.Width,
		height:      normalized.Height,
		positionSet: normalized.PositionSet,
		x:           normalized.X,
		y:           normalized.Y,
		mode:        PetWindowPassive,
		// 桌宠默认置顶；窗口仍保持点击穿透和不可激活，置顶只负责 Z-order，
		// 不应改变用户当前正在使用的应用焦点。
		alwaysOnTop: true,
	}
	// 系统关闭事件由原生 driver 反向通知状态机；主动关闭由宿主自身完成状态落盘，
	// driver 会抑制同一窗口代次的同步 hook 回调，避免和宿主锁形成死锁。
	driver.SetWindowClosedCallback(window.handleDriverClosed)
	return window, nil
}

func normalizePetWindowOptions(options PetWindowOptions) (PetWindowOptions, error) {
	options.Name = strings.TrimSpace(options.Name)
	if options.Name == "" {
		options.Name = DefaultPetWindowName
	}
	options.Title = strings.TrimSpace(options.Title)
	if options.Title == "" {
		options.Title = DefaultPetWindowTitle
	}
	options.URL = strings.TrimSpace(options.URL)

	// 0 使用稳定默认尺寸；负数通常意味着调用方把坐标/尺寸参数传错，直接失败而不是制造半残窗口。
	if options.Width == 0 {
		options.Width = DefaultPetWindowWidth
	}
	if options.Height == 0 {
		options.Height = DefaultPetWindowHeight
	}
	if options.Width < 0 || options.Height < 0 {
		return PetWindowOptions{}, ErrPetWindowInvalidSize
	}
	return options, nil
}

// Open 创建并显示窗口；重复调用不会重复创建原生窗口，但会把最新的
// WorkArea 几何和交互配置交给 driver 重新应用，修复宿主重启/热更新后
// 原生窗口残留旧尺寸而前端只能困在局部区域的问题。
func (w *PetWindow) Open() error {
	if w == nil {
		return ErrPetWindowNilDriver
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	return w.openLocked()
}

func (w *PetWindow) openLocked() error {
	if err := w.driver.Open(petWindowOpenConfig{
		Name:              w.name,
		Title:             w.title,
		URL:               w.url,
		Width:             w.width,
		Height:            w.height,
		PositionSet:       w.positionSet,
		X:                 w.x,
		Y:                 w.y,
		AlwaysOnTop:       w.alwaysOnTop,
		IgnoreMouseEvents: petWindowModeClickThrough(w.mode),
	}); err != nil {
		return fmt.Errorf("open pet window: %w", err)
	}
	w.open = true

	// keyboard 模式需要真实的 Focus；Wails 没有 showInactive 或 setFocusable，不能伪造“后台聚焦”。
	if w.mode == PetWindowKeyboard {
		if err := w.driver.Focus(); err != nil {
			_ = w.driver.Close()
			w.open = false
			return fmt.Errorf("focus pet window after open: %w", err)
		}
	}
	return nil
}

// Close 关闭原生窗口；关闭后保留模式、尺寸和位置配置，下一次 Open 会按最新配置重建。
func (w *PetWindow) Close() error {
	if w == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.open {
		return nil
	}
	if err := w.driver.Close(); err != nil {
		return fmt.Errorf("close pet window: %w", err)
	}
	w.open = false
	return nil
}

// Toggle 在打开和关闭之间切换；锁保证并发调用不会创建多个窗口。
func (w *PetWindow) Toggle() error {
	if w == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.open {
		if err := w.driver.Close(); err != nil {
			return fmt.Errorf("toggle close pet window: %w", err)
		}
		w.open = false
		return nil
	}
	return w.openLocked()
}

// handleDriverClosed 收敛系统关闭路径和宿主状态。
// 只有 driver 确认当前窗口实例正在关闭时才会调用这里；旧窗口回调由 driver 丢弃，
// 防止“关闭旧窗口后马上重开”时旧事件把新窗口标记成已关闭。
func (w *PetWindow) handleDriverClosed() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.open = false
}

func (w *PetWindow) IsOpen() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.open
}

// SetMode 将点击穿透和键盘聚焦收敛到一个状态，避免调用方分别维护多个原生开关。
func (w *PetWindow) SetMode(mode PetWindowMode) error {
	if !isValidPetWindowMode(mode) {
		return fmt.Errorf("%w: %q", ErrPetWindowInvalidMode, mode)
	}
	if w == nil {
		return ErrPetWindowNilDriver
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.mode == mode {
		return nil
	}

	if w.open {
		previousClickThrough := petWindowModeClickThrough(w.mode)
		previousMode := w.mode
		if previousMode == PetWindowKeyboard && mode != PetWindowKeyboard {
			if err := w.driver.ReleaseFocus(); err != nil {
				return fmt.Errorf("release pet window focus: %w", err)
			}
		}
		if previousClickThrough && !petWindowModeClickThrough(mode) {
			// 必须在解除点击穿透前记录外部前台窗口；解除后 Windows 可能已经把桌宠
			// 视为当前前台，再记录就无法恢复用户原来的输入目标。
			w.driver.CaptureFocusRestore()
		}
		if err := w.driver.SetIgnoreMouseEvents(petWindowModeClickThrough(mode)); err != nil {
			return fmt.Errorf("set pet window mouse mode: %w", err)
		}
		if mode == PetWindowKeyboard {
			if err := w.driver.Focus(); err != nil {
				// 聚焦失败时恢复旧的鼠标状态，避免状态机和原生窗口分裂。
				_ = w.driver.SetIgnoreMouseEvents(previousClickThrough)
				return fmt.Errorf("focus pet window for keyboard mode: %w", err)
			}
		}
		w.mode = mode
		return nil
	}

	w.mode = mode
	return nil
}

func (w *PetWindow) Mode() PetWindowMode {
	if w == nil {
		return PetWindowPassive
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.mode
}

// Move 更新绝对屏幕坐标。窗口未打开时只缓存位置，供下一次 Open 使用。
func (w *PetWindow) Move(x, y int) error {
	if w == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.positionSet && w.x == x && w.y == y {
		return nil
	}
	if w.open {
		if err := w.driver.SetPosition(x, y); err != nil {
			return fmt.Errorf("move pet window: %w", err)
		}
	}
	w.positionSet = true
	w.x = x
	w.y = y
	return nil
}

// Resize 更新窗口尺寸。Wails 只提供 SetSize，不提供 Electron 的可拖拽/自动布局能力。
func (w *PetWindow) Resize(width, height int) error {
	if width <= 0 || height <= 0 {
		return ErrPetWindowInvalidSize
	}
	if w == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.width == width && w.height == height {
		return nil
	}
	if w.open {
		if err := w.driver.SetSize(width, height); err != nil {
			return fmt.Errorf("resize pet window: %w", err)
		}
	}
	w.width = width
	w.height = height
	return nil
}

// Focus 请求窗口获得焦点。未打开时不创建窗口，调用方需要显式 Open 后再 Focus。
func (w *PetWindow) Focus() error {
	if w == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.open {
		return nil
	}
	if err := w.driver.Focus(); err != nil {
		return fmt.Errorf("focus pet window: %w", err)
	}
	return nil
}

// SetAlwaysOnTop 设置置顶状态；默认值为 true，关闭期间的变更会在下次 Open 生效。
func (w *PetWindow) SetAlwaysOnTop(alwaysOnTop bool) error {
	if w == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.alwaysOnTop == alwaysOnTop {
		return nil
	}
	if w.open {
		if err := w.driver.SetAlwaysOnTop(alwaysOnTop); err != nil {
			return fmt.Errorf("set pet window always-on-top: %w", err)
		}
	}
	w.alwaysOnTop = alwaysOnTop
	return nil
}

// State 返回给主控/绑定层的稳定快照。Focused 只反映 Wails 能真实查询到的焦点状态。
func (w *PetWindow) State() PetWindowState {
	state := PetWindowState{
		Version:      PetSchemaVersion,
		Mode:         PetWindowPassive,
		ClickThrough: true,
	}
	if w == nil {
		return state
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	state.Open = w.open
	state.Mode = w.mode
	state.ClickThrough = petWindowModeClickThrough(w.mode)
	state.AlwaysOnTop = w.alwaysOnTop
	if w.open {
		state.Focused = w.driver.IsFocused()
	}
	return state
}

func isValidPetWindowMode(mode PetWindowMode) bool {
	switch mode {
	case PetWindowPassive, PetWindowInteractive, PetWindowKeyboard:
		return true
	default:
		return false
	}
}

func petWindowModeClickThrough(mode PetWindowMode) bool {
	return mode == PetWindowPassive
}
