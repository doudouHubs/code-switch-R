package services

import (
	"errors"
	"sync"
	"testing"
)

type fakePetWindowDriver struct {
	mu sync.Mutex

	openConfigs       []petWindowOpenConfig
	closeCalls        int
	focusCalls        int
	releaseFocusCalls int
	captureFocusCalls int
	closedFn          func()

	ignoreMouseCalls []bool
	positionCalls    [][2]int
	sizeCalls        [][2]int
	topCalls         []bool

	opened          bool
	focused         bool
	focusErr        error
	releaseFocusErr error
	openErr         error
	closeErr        error
	ignoreErr       error
	positionErr     error
	sizeErr         error
	topErr          error
}

func (f *fakePetWindowDriver) Open(config petWindowOpenConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openErr != nil {
		return f.openErr
	}
	if f.opened {
		return nil
	}
	f.opened = true
	f.openConfigs = append(f.openConfigs, config)
	f.focused = false
	return nil
}

func (f *fakePetWindowDriver) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closeErr != nil {
		return f.closeErr
	}
	if !f.opened {
		return nil
	}
	f.opened = false
	f.closeCalls++
	f.focused = false
	return nil
}

func (f *fakePetWindowDriver) SetWindowClosedCallback(callback func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closedFn = callback
}

func (f *fakePetWindowDriver) CaptureFocusRestore() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captureFocusCalls++
}

func (f *fakePetWindowDriver) emitWindowClosed() {
	f.mu.Lock()
	callback := f.closedFn
	f.opened = false
	f.focused = false
	f.mu.Unlock()
	if callback != nil {
		callback()
	}
}

func (f *fakePetWindowDriver) SetIgnoreMouseEvents(ignore bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ignoreErr != nil {
		return f.ignoreErr
	}
	f.ignoreMouseCalls = append(f.ignoreMouseCalls, ignore)
	return nil
}

func (f *fakePetWindowDriver) Focus() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.focusCalls++
	if f.focusErr != nil {
		return f.focusErr
	}
	f.focused = true
	return nil
}

func (f *fakePetWindowDriver) ReleaseFocus() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseFocusCalls++
	if f.releaseFocusErr != nil {
		return f.releaseFocusErr
	}
	f.focused = false
	return nil
}

func (f *fakePetWindowDriver) SetPosition(x, y int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.positionErr != nil {
		return f.positionErr
	}
	f.positionCalls = append(f.positionCalls, [2]int{x, y})
	return nil
}

func (f *fakePetWindowDriver) SetSize(width, height int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sizeErr != nil {
		return f.sizeErr
	}
	f.sizeCalls = append(f.sizeCalls, [2]int{width, height})
	return nil
}

func (f *fakePetWindowDriver) SetAlwaysOnTop(alwaysOnTop bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.topErr != nil {
		return f.topErr
	}
	f.topCalls = append(f.topCalls, alwaysOnTop)
	return nil
}

func (f *fakePetWindowDriver) IsFocused() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.focused
}

func newTestPetWindow(t *testing.T) (*PetWindow, *fakePetWindowDriver) {
	t.Helper()
	driver := &fakePetWindowDriver{}
	window, err := newPetWindowWithDriver(driver, PetWindowOptions{
		Name:   "test-pet",
		Title:  "Test Pet",
		URL:    "/pet-test",
		Width:  320,
		Height: 240,
	})
	if err != nil {
		t.Fatalf("newPetWindowWithDriver() error = %v", err)
	}
	return window, driver
}

func TestPetWindowOpenCloseToggleAreIdempotent(t *testing.T) {
	window, driver := newTestPetWindow(t)

	state := window.State()
	if state.Open || state.Mode != PetWindowPassive || !state.ClickThrough || state.AlwaysOnTop {
		t.Fatalf("initial state = %#v", state)
	}

	if err := window.Open(); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := window.Open(); err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	if !window.IsOpen() || len(driver.openConfigs) != 1 {
		t.Fatalf("open state/calls = %v/%d", window.IsOpen(), len(driver.openConfigs))
	}
	config := driver.openConfigs[0]
	if !config.IgnoreMouseEvents || config.AlwaysOnTop || config.Width != 320 || config.Height != 240 {
		t.Fatalf("open config = %#v", config)
	}

	if err := window.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := window.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if window.IsOpen() || driver.closeCalls != 1 {
		t.Fatalf("close state/calls = %v/%d", window.IsOpen(), driver.closeCalls)
	}

	if err := window.Toggle(); err != nil {
		t.Fatalf("Toggle() open error = %v", err)
	}
	if err := window.Toggle(); err != nil {
		t.Fatalf("Toggle() close error = %v", err)
	}
	if driver.closeCalls != 2 || len(driver.openConfigs) != 2 {
		t.Fatalf("toggle calls = open:%d close:%d", len(driver.openConfigs), driver.closeCalls)
	}
}

func TestPetWindowModesControlMouseAndFocus(t *testing.T) {
	window, driver := newTestPetWindow(t)
	if err := window.Open(); err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if err := window.SetMode(PetWindowInteractive); err != nil {
		t.Fatalf("SetMode(interactive) error = %v", err)
	}
	if driver.focusCalls != 0 || len(driver.ignoreMouseCalls) != 1 || driver.ignoreMouseCalls[0] {
		t.Fatalf("interactive calls = ignore:%v focus:%d", driver.ignoreMouseCalls, driver.focusCalls)
	}
	if err := window.SetMode(PetWindowInteractive); err != nil {
		t.Fatalf("repeat SetMode(interactive) error = %v", err)
	}
	if len(driver.ignoreMouseCalls) != 1 {
		t.Fatalf("repeat interactive should be idempotent: %v", driver.ignoreMouseCalls)
	}

	if err := window.SetMode(PetWindowKeyboard); err != nil {
		t.Fatalf("SetMode(keyboard) error = %v", err)
	}
	if driver.focusCalls != 1 || len(driver.ignoreMouseCalls) != 2 || driver.ignoreMouseCalls[1] {
		t.Fatalf("keyboard calls = ignore:%v focus:%d", driver.ignoreMouseCalls, driver.focusCalls)
	}
	if state := window.State(); state.Mode != PetWindowKeyboard || state.ClickThrough || !state.Focused {
		t.Fatalf("keyboard state = %#v", state)
	}

	if err := window.SetMode(PetWindowPassive); err != nil {
		t.Fatalf("SetMode(passive) error = %v", err)
	}
	if len(driver.ignoreMouseCalls) != 3 || !driver.ignoreMouseCalls[2] {
		t.Fatalf("passive calls = %v", driver.ignoreMouseCalls)
	}
	if state := window.State(); state.Mode != PetWindowPassive || !state.ClickThrough || state.Focused {
		t.Fatalf("passive state = %#v", state)
	}
	if driver.releaseFocusCalls != 1 {
		t.Fatalf("release focus calls = %d", driver.releaseFocusCalls)
	}
	if driver.captureFocusCalls != 1 {
		t.Fatalf("capture focus calls = %d", driver.captureFocusCalls)
	}
}

func TestPetWindowMoveResizeAndTopmostCacheWhileClosed(t *testing.T) {
	window, driver := newTestPetWindow(t)

	if err := window.Move(-100, 80); err != nil {
		t.Fatalf("Move() error = %v", err)
	}
	if err := window.Resize(500, 360); err != nil {
		t.Fatalf("Resize() error = %v", err)
	}
	if err := window.SetAlwaysOnTop(false); err != nil {
		t.Fatalf("SetAlwaysOnTop() error = %v", err)
	}
	if len(driver.positionCalls) != 0 || len(driver.sizeCalls) != 0 || len(driver.topCalls) != 0 {
		t.Fatalf("closed window should cache operations: position=%v size=%v top=%v", driver.positionCalls, driver.sizeCalls, driver.topCalls)
	}

	if err := window.Open(); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	config := driver.openConfigs[0]
	if !config.PositionSet || config.X != -100 || config.Y != 80 || config.Width != 500 || config.Height != 360 || config.AlwaysOnTop {
		t.Fatalf("cached open config = %#v", config)
	}

	if err := window.Move(-100, 80); err != nil {
		t.Fatalf("repeat Move() error = %v", err)
	}
	if err := window.Resize(500, 360); err != nil {
		t.Fatalf("repeat Resize() error = %v", err)
	}
	if err := window.SetAlwaysOnTop(false); err != nil {
		t.Fatalf("repeat SetAlwaysOnTop() error = %v", err)
	}
	if len(driver.positionCalls) != 0 || len(driver.sizeCalls) != 0 || len(driver.topCalls) != 0 {
		t.Fatalf("repeat same values should be idempotent: position=%v size=%v top=%v", driver.positionCalls, driver.sizeCalls, driver.topCalls)
	}

	if err := window.Move(20, 30); err != nil {
		t.Fatalf("second Move() error = %v", err)
	}
	if err := window.Resize(510, 370); err != nil {
		t.Fatalf("second Resize() error = %v", err)
	}
	if err := window.SetAlwaysOnTop(true); err != nil {
		t.Fatalf("second SetAlwaysOnTop() error = %v", err)
	}
	if len(driver.positionCalls) != 1 || len(driver.sizeCalls) != 1 || len(driver.topCalls) != 1 {
		t.Fatalf("changed values should reach driver: position=%v size=%v top=%v", driver.positionCalls, driver.sizeCalls, driver.topCalls)
	}

	if err := window.Resize(0, 100); !errors.Is(err, ErrPetWindowInvalidSize) {
		t.Fatalf("Resize(0, 100) error = %v", err)
	}
	if err := window.SetMode(PetWindowMode("unknown")); !errors.Is(err, ErrPetWindowInvalidMode) {
		t.Fatalf("invalid mode error = %v", err)
	}
}

func TestPetWindowKeyboardOpenFailureRollsBack(t *testing.T) {
	driver := &fakePetWindowDriver{focusErr: errors.New("focus denied")}
	window, err := newPetWindowWithDriver(driver, PetWindowOptions{Width: 320, Height: 240})
	if err != nil {
		t.Fatalf("newPetWindowWithDriver() error = %v", err)
	}
	if err := window.SetMode(PetWindowKeyboard); err != nil {
		t.Fatalf("SetMode(keyboard) while closed error = %v", err)
	}
	if err := window.Open(); err == nil {
		t.Fatal("Open() error = nil, want focus error")
	}
	if window.IsOpen() || driver.closeCalls != 1 {
		t.Fatalf("failed keyboard open state/calls = %v/%d", window.IsOpen(), driver.closeCalls)
	}
}

func TestPetWindowConcurrentOpenAndCloseAreSerialized(t *testing.T) {
	window, driver := newTestPetWindow(t)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := window.Open(); err != nil {
				t.Errorf("concurrent Open() error = %v", err)
			}
		}()
	}
	wg.Wait()
	if !window.IsOpen() || len(driver.openConfigs) != 1 {
		t.Fatalf("concurrent open state/calls = %v/%d", window.IsOpen(), len(driver.openConfigs))
	}

	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := window.Close(); err != nil {
				t.Errorf("concurrent Close() error = %v", err)
			}
		}()
	}
	wg.Wait()
	if window.IsOpen() || driver.closeCalls != 1 {
		t.Fatalf("concurrent close state/calls = %v/%d", window.IsOpen(), driver.closeCalls)
	}
}

func TestPetWindowSystemCloseSynchronizesStateAndAllowsReopen(t *testing.T) {
	window, driver := newTestPetWindow(t)
	if err := window.Open(); err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	driver.emitWindowClosed()
	if window.IsOpen() {
		t.Fatal("system close should mark PetWindow closed")
	}
	if state := window.State(); state.Open {
		t.Fatalf("state after system close = %#v", state)
	}

	if err := window.Open(); err != nil {
		t.Fatalf("reopen after system close error = %v", err)
	}
	if !window.IsOpen() || len(driver.openConfigs) != 2 {
		t.Fatalf("reopen state/calls = %v/%d", window.IsOpen(), len(driver.openConfigs))
	}
}
