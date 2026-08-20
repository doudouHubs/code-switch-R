package services

import "errors"

var (
	ErrPetWindowAPIUnavailable  = errors.New("pet window api requires a pet window")
	ErrPetWindowIdleUnsupported = errors.New("pet window idle time is unsupported on this platform")
)

// PetWindowAPI 只把原生窗口状态机暴露给 Wails，不参与宠物设置持久化。
// 配置落盘由 PetService 负责，调用方在确认保存成功后再调用这里的生命周期方法。
type PetWindowAPI struct {
	window *PetWindow
}

func NewPetWindowAPI(window *PetWindow) *PetWindowAPI {
	return &PetWindowAPI{window: window}
}

func (a *PetWindowAPI) Open() error {
	window, err := a.getWindow()
	if err != nil {
		return err
	}
	return window.Open()
}

func (a *PetWindowAPI) Close() error {
	window, err := a.getWindow()
	if err != nil {
		return err
	}
	return window.Close()
}

func (a *PetWindowAPI) Toggle() error {
	window, err := a.getWindow()
	if err != nil {
		return err
	}
	return window.Toggle()
}

// 以下方法只负责 Wails 参数适配和统一 nil 兜底，具体窗口行为仍由 PetWindow 状态机决定。
func (a *PetWindowAPI) SetMode(mode string) error {
	window, err := a.getWindow()
	if err != nil {
		return err
	}
	return window.SetMode(PetWindowMode(mode))
}

func (a *PetWindowAPI) Move(x, y int) error {
	window, err := a.getWindow()
	if err != nil {
		return err
	}
	return window.Move(x, y)
}

// IdleSeconds 返回系统级最后输入到当前时刻的秒数。
// 该能力不属于窗口 driver：Windows 需要直接读取 user32，全局窗口状态机不应被平台 API 污染。
func (a *PetWindowAPI) IdleSeconds() (int, error) {
	if _, err := a.getWindow(); err != nil {
		return 0, err
	}
	return readPetWindowIdleSeconds()
}

func (a *PetWindowAPI) Resize(width, height int) error {
	window, err := a.getWindow()
	if err != nil {
		return err
	}
	return window.Resize(width, height)
}

func (a *PetWindowAPI) Focus() error {
	window, err := a.getWindow()
	if err != nil {
		return err
	}
	return window.Focus()
}

func (a *PetWindowAPI) SetAlwaysOnTop(alwaysOnTop bool) error {
	window, err := a.getWindow()
	if err != nil {
		return err
	}
	return window.SetAlwaysOnTop(alwaysOnTop)
}

// SetPlatformLayer 将桌宠放到指定外部窗口上方；空字符串表示桌面普通层级。
func (a *PetWindowAPI) SetPlatformLayer(platformID string) error {
	window, err := a.getWindow()
	if err != nil {
		return err
	}
	return window.SetPlatformLayer(platformID)
}

// GetPlatforms 返回当前桌宠所在工作区内的外部窗口平台。
// 平台检测失败时由平台 driver 返回 Available=false；这条增强能力不能阻断桌宠本身运行。
func (a *PetWindowAPI) GetPlatforms() (PetWindowPlatformSnapshot, error) {
	window, err := a.getWindow()
	if err != nil {
		return PetWindowPlatformSnapshot{}, err
	}
	return window.GetPlatforms()
}

func (a *PetWindowAPI) State() PetWindowState {
	if a == nil || a.window == nil {
		return PetWindowState{
			Version:      PetSchemaVersion,
			Mode:         PetWindowPassive,
			ClickThrough: true,
		}
	}
	return a.window.State()
}

func (a *PetWindowAPI) getWindow() (*PetWindow, error) {
	if a == nil || a.window == nil {
		return nil, ErrPetWindowAPIUnavailable
	}
	return a.window, nil
}
