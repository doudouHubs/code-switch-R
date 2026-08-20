//go:build windows

package services

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/sys/windows"
)

const petWindowPlatformsChangedEvent = "pet.window.platforms.changed"

const (
	petPlatformEventObjectMin        = 0x8001
	petPlatformEventObjectMax        = 0x800B
	petPlatformEventSystemForeground = 0x0003
	petPlatformEventSystemMoveStart  = 0x000A
	petPlatformEventSystemMoveEnd    = 0x000B
	petPlatformEventSystemMinimize   = 0x0016
	petPlatformEventSystemRestore    = 0x0017
	petPlatformWinEventOutOfContext  = 0x0000
	petPlatformWinEventSkipOwnProc   = 0x0002
	petPlatformObjectWindow          = 0
	petPlatformIndexContainer        = 0
	petPlatformDWMAttributeCloaked   = 14
)

var (
	petPlatformUser32              = windows.NewLazySystemDLL("user32.dll")
	petPlatformSetWinEventHookProc = petPlatformUser32.NewProc("SetWinEventHook")
	petPlatformUnhookWinEventProc  = petPlatformUser32.NewProc("UnhookWinEvent")
	petPlatformIsIconicProc        = petPlatformUser32.NewProc("IsIconic")
)

// petWindowPlatformMonitor 只负责 Win32 平台候选的发现和失效通知，不持有宠物动画状态。
// 这样窗口事件即使高频到达，也不会跨线程直接修改 Vue 的 renderedPetX/renderedPetLift。
type petWindowPlatformMonitor struct {
	mu             sync.Mutex
	app            *application.App
	hwnd           windows.HWND
	ownPID         uint32
	hooks          []uintptr
	callback       uintptr
	started        bool
	available      bool
	revision       uint64
	movingWindowID string
}

func newPetWindowPlatformMonitor(app *application.App) *petWindowPlatformMonitor {
	return &petWindowPlatformMonitor{app: app}
}

func (m *petWindowPlatformMonitor) Start(hwnd windows.HWND) error {
	if m == nil || hwnd == 0 {
		return fmt.Errorf("pet platform monitor requires a window handle")
	}

	m.mu.Lock()
	if m.started && m.hwnd == hwnd && m.available {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	m.Stop()

	callback := windows.NewCallback(func(
		hook uintptr,
		event uintptr,
		eventWindow uintptr,
		idObject uintptr,
		idChild uintptr,
		eventThread uintptr,
		eventTick uintptr,
	) uintptr {
		return m.handleWinEvent(hook, event, eventWindow, idObject, idChild, eventThread, eventTick)
	})
	flags := uintptr(petPlatformWinEventOutOfContext | petPlatformWinEventSkipOwnProc)
	objectHook, _, objectErr := petPlatformSetWinEventHookProc.Call(
		petPlatformEventObjectMin,
		petPlatformEventObjectMax,
		0,
		callback,
		0,
		0,
		flags,
	)
	if objectHook == 0 {
		return fmt.Errorf("SetWinEventHook object events failed: %v", objectErr)
	}

	systemHook, _, systemErr := petPlatformSetWinEventHookProc.Call(
		petPlatformEventSystemForeground,
		petPlatformEventSystemRestore,
		0,
		callback,
		0,
		0,
		flags,
	)
	if systemHook == 0 {
		petPlatformUnhookWinEventProc.Call(objectHook)
		return fmt.Errorf("SetWinEventHook system events failed: %v", systemErr)
	}

	m.mu.Lock()
	m.hwnd = hwnd
	m.ownPID = windows.GetCurrentProcessId()
	m.hooks = []uintptr{objectHook, systemHook}
	m.callback = callback
	m.started = true
	m.available = true
	m.revision++
	m.mu.Unlock()
	return nil
}

func (m *petWindowPlatformMonitor) Stop() {
	if m == nil {
		return
	}

	m.mu.Lock()
	hooks := append([]uintptr(nil), m.hooks...)
	m.hooks = nil
	m.callback = 0
	m.started = false
	m.available = false
	m.hwnd = 0
	m.ownPID = 0
	m.movingWindowID = ""
	m.mu.Unlock()

	if len(hooks) == 0 {
		return
	}

	unhook := func() {
		for _, hook := range hooks {
			if hook != 0 {
				petPlatformUnhookWinEventProc.Call(hook)
			}
		}
	}

	// OUTOFCONTEXT hook 的回调线程就是注册 hook 的线程，而注册发生在
	// Wails 主线程。关闭/复用通常从 binding goroutine 进入；如果直接在该
	// goroutine 调 Unhook，生命周期就会跨线程操作同一条 Win32 消息队列。
	// 先清掉 started 状态让已经排队的回调快速退出，再把真正的解绑派发回
	// 主线程；应用尚未建立全局主线程时才允许直接解绑（仅用于异常/测试兜底）。
	if m.app != nil && application.Get() == m.app {
		_ = application.InvokeSyncWithError(func() error {
			unhook()
			return nil
		})
		return
	}
	unhook()
}

func (m *petWindowPlatformMonitor) GetPlatforms() (PetWindowPlatformSnapshot, error) {
	if m == nil {
		return PetWindowPlatformSnapshot{}, nil
	}

	m.mu.Lock()
	hwnd := m.hwnd
	ownPID := m.ownPID
	available := m.available && m.started
	movingWindowID := m.movingWindowID
	m.mu.Unlock()
	if !available || hwnd == 0 {
		return PetWindowPlatformSnapshot{}, nil
	}

	overlay, err := readPetWindowRect(hwnd)
	if err != nil {
		return PetWindowPlatformSnapshot{}, fmt.Errorf("read pet overlay rect: %w", err)
	}
	platforms, occluders, err := enumeratePetWindowPlatforms(hwnd, ownPID, overlay)
	if err != nil {
		return PetWindowPlatformSnapshot{}, err
	}
	return PetWindowPlatformSnapshot{
		Available:      true,
		Overlay:        petPlatformRect(overlay),
		Platforms:      platforms,
		Occluders:      occluders,
		MovingWindowID: movingWindowID,
	}, nil
}

func (m *petWindowPlatformMonitor) handleWinEvent(
	hook uintptr,
	event uintptr,
	hwnd uintptr,
	idObject uintptr,
	idChild uintptr,
	eventThread uintptr,
	eventTick uintptr,
) uintptr {
	_ = hook
	_ = eventThread
	_ = eventTick
	if hwnd == 0 {
		return 0
	}
	eventID := uint32(event)
	isObjectEvent := eventID >= petPlatformEventObjectMin && eventID <= petPlatformEventObjectMax
	if isObjectEvent &&
		(idObject != petPlatformObjectWindow || idChild != petPlatformIndexContainer) {
		return 0
	}
	if !isObjectEvent && eventID != petPlatformEventSystemForeground &&
		eventID != petPlatformEventSystemMoveStart && eventID != petPlatformEventSystemMoveEnd &&
		eventID != petPlatformEventSystemMinimize && eventID != petPlatformEventSystemRestore {
		return 0
	}

	eventHWND := windows.HWND(hwnd)
	var eventPID uint32
	_, _ = windows.GetWindowThreadProcessId(eventHWND, &eventPID)

	m.mu.Lock()
	if !m.started || !m.available || eventHWND == m.hwnd || eventPID == m.ownPID {
		m.mu.Unlock()
		return 0
	}
	app := m.app
	if eventID == petPlatformEventSystemMoveStart {
		m.movingWindowID = fmt.Sprintf("%#x", uintptr(eventHWND))
	} else if eventID == petPlatformEventSystemMoveEnd &&
		m.movingWindowID == fmt.Sprintf("%#x", uintptr(eventHWND)) {
		// 只清理同一个窗口的结束事件；另一个窗口紧接着开始拖动时，
		// 旧窗口的延迟结束事件不能把新的 movingWindowID 抹掉。
		m.movingWindowID = ""
	}
	movingWindowID := m.movingWindowID
	m.revision++
	revision := m.revision
	m.mu.Unlock()

	if app != nil {
		// 事件只表达“缓存失效”，窗口矩形仍由下一次 GetPlatforms 读取，
		// 避免把 WinEvent 回调线程变成第二个平台状态 owner。
		app.Event.Emit(petWindowPlatformsChangedEvent, map[string]any{
			"revision":       revision,
			"movingWindowId": movingWindowID,
		})
	}
	return 0
}

func enumeratePetWindowPlatforms(petHWND windows.HWND, ownPID uint32, overlay petWindowRect) ([]PetWindowPlatform, []PetWindowPlatform, error) {
	platforms := make([]PetWindowPlatform, 0, 16)
	occluders := make([]PetWindowPlatform, 0, 8)
	zOrder := 0
	enumErr := windows.EnumWindows(syscall.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		rect, ok := readPetWindowCandidateRect(hwnd, petHWND, ownPID, overlay)
		if !ok {
			return 1
		}

		item := PetWindowPlatform{
			ID:   fmt.Sprintf("%#x", uintptr(hwnd)),
			Rect: petPlatformRect(rect),
			// EnumWindows 按 Z 序从前到后回调；这个序号必须在平台/遮挡
			// 分类前统一分配，否则过滤掉的窗口会导致前端误判遮挡关系。
			ZOrder: zOrder,
		}
		zOrder++
		if rect.top >= overlay.top && rect.top < overlay.bottom {
			platforms = append(platforms, item)
		} else if rect.top < overlay.top && rect.bottom > overlay.top {
			// 窗口顶部在工作区外但窗口主体压入工作区时，它不能作为落脚
			// 平台，却会遮住位于其下方的窗口顶部，必须保留几何和 Z 序。
			occluders = append(occluders, item)
		}
		return 1
	}), nil)
	if enumErr != nil && !errors.Is(enumErr, syscall.Errno(0)) {
		return nil, nil, fmt.Errorf("EnumWindows failed: %w", enumErr)
	}
	return platforms, occluders, nil
}

func readPetWindowCandidateRect(hwnd, petHWND windows.HWND, ownPID uint32, overlay petWindowRect) (petWindowRect, bool) {
	if hwnd == 0 || hwnd == petHWND || !windows.IsWindowVisible(hwnd) {
		return petWindowRect{}, false
	}

	var pid uint32
	_, _ = windows.GetWindowThreadProcessId(hwnd, &pid)
	if pid == 0 || pid == ownPID || petPlatformIsSystemSurface(hwnd) || petPlatformIsIconic(hwnd) || petPlatformIsCloaked(hwnd) {
		return petWindowRect{}, false
	}

	rect, err := readPetWindowRect(hwnd)
	if err != nil || rect.right <= rect.left || rect.bottom <= rect.top {
		return petWindowRect{}, false
	}
	if rect.right <= overlay.left || rect.left >= overlay.right ||
		rect.bottom <= overlay.top || rect.top >= overlay.bottom {
		return petWindowRect{}, false
	}
	return rect, true
}

func petPlatformIsIconic(hwnd windows.HWND) bool {
	value, _, _ := petPlatformIsIconicProc.Call(uintptr(hwnd))
	return value != 0
}

func petPlatformIsCloaked(hwnd windows.HWND) bool {
	var cloaked uint32
	err := windows.DwmGetWindowAttribute(
		hwnd,
		petPlatformDWMAttributeCloaked,
		unsafe.Pointer(&cloaked),
		uint32(unsafe.Sizeof(cloaked)),
	)
	return err == nil && cloaked != 0
}

func petPlatformIsSystemSurface(hwnd windows.HWND) bool {
	if hwnd == windows.GetDesktopWindow() || hwnd == windows.GetShellWindow() {
		return true
	}

	buffer := make([]uint16, 128)
	length, _ := windows.GetClassName(hwnd, &buffer[0], int32(len(buffer)))
	className := strings.TrimSpace(windows.UTF16ToString(buffer[:length]))
	switch className {
	case "Progman", "WorkerW", "Shell_TrayWnd", "Shell_SecondaryTrayWnd", "MSTaskSwWClass":
		return true
	default:
		return false
	}
}

func petPlatformRect(rect petWindowRect) PetWindowPlatformRect {
	return PetWindowPlatformRect{
		Left:   rect.left,
		Top:    rect.top,
		Right:  rect.right,
		Bottom: rect.bottom,
	}
}
