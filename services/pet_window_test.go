package services

import (
	"errors"
	"sync"
	"testing"
)

type fakePetWindowDriver struct {
	mu sync.Mutex

	openConfigs       []petWindowOpenConfig
	openCalls         int
	closeCalls        int
	focusCalls        int
	releaseFocusCalls int
	captureFocusCalls int
	closedFn          func()

	ignoreMouseCalls   []bool
	positionCalls      [][2]int
	sizeCalls          [][2]int
	topCalls           []bool
	platformLayerCalls []string
	platformSnapshot   PetWindowPlatformSnapshot
	platformErr        error

	opened               bool
	focused              bool
	focusErr             error
	releaseFocusErr      error
	openErr              error
	closeErr             error
	ignoreErr            error
	positionErr          error
	sizeErr              error
	topErr               error
	platformLayerTopmost bool
	platformLayerErr     error
}

func (f *fakePetWindowDriver) Open(config petWindowOpenConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.openCalls++
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

func (f *fakePetWindowDriver) SetPlatformLayer(platformID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.platformLayerErr != nil {
		return false, f.platformLayerErr
	}
	f.platformLayerCalls = append(f.platformLayerCalls, platformID)
	if platformID == "" {
		return true, nil
	}
	return f.platformLayerTopmost, nil
}

func (f *fakePetWindowDriver) GetPlatforms() (PetWindowPlatformSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.platformErr != nil {
		return PetWindowPlatformSnapshot{}, f.platformErr
	}
	return f.platformSnapshot, nil
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

func TestApplyPetWindowWorkAreaConfigCoversEntireWorkArea(t *testing.T) {
	config := applyPetWindowWorkAreaConfig(petWindowOpenConfig{
		Name:              "test-pet",
		Title:             "Test Pet",
		URL:               "/pet-test",
		Width:             420,
		Height:            380,
		PositionSet:       false,
		X:                 12,
		Y:                 34,
		AlwaysOnTop:       true,
		IgnoreMouseEvents: true,
	}, -1920, 0, 3840, 2160)

	if config.Width != 3840 || config.Height != 2160 {
		t.Fatalf("work area size = %dx%d, want 3840x2160", config.Width, config.Height)
	}
	if !config.PositionSet || config.X != -1920 || config.Y != 0 {
		t.Fatalf("work area position = set:%v (%d,%d), want set:true (-1920,0)", config.PositionSet, config.X, config.Y)
	}
	if !config.AlwaysOnTop || !config.IgnoreMouseEvents || config.Name != "test-pet" || config.URL != "/pet-test" {
		t.Fatalf("work area config changed unrelated fields: %#v", config)
	}
}

func TestPetWindowOpenCloseToggleAreIdempotent(t *testing.T) {
	window, driver := newTestPetWindow(t)

	state := window.State()
	if state.Open || state.Mode != PetWindowPassive || !state.ClickThrough || !state.AlwaysOnTop {
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
	if driver.openCalls != 2 {
		t.Fatalf("repeated Open() should refresh the existing driver window, calls = %d", driver.openCalls)
	}
	config := driver.openConfigs[0]
	if !config.IgnoreMouseEvents || !config.AlwaysOnTop || config.Width != 320 || config.Height != 240 {
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
	if driver.captureFocusCalls != 2 {
		t.Fatalf("capture focus calls = %d, want interactive and keyboard transitions", driver.captureFocusCalls)
	}
}

func TestPetWindowPassiveModeStillAppliesClickThroughWhenReleaseFocusFails(t *testing.T) {
	window, driver := newTestPetWindow(t)
	if err := window.Open(); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := window.SetMode(PetWindowKeyboard); err != nil {
		t.Fatalf("SetMode(keyboard) error = %v", err)
	}

	driver.releaseFocusErr = errors.New("foreground restore denied")
	if err := window.SetMode(PetWindowPassive); err != nil {
		t.Fatalf("SetMode(passive) should keep the click-through state even when focus release fails: %v", err)
	}
	if len(driver.ignoreMouseCalls) != 2 || !driver.ignoreMouseCalls[1] {
		t.Fatalf("passive mode mouse calls = %v, want the final call to enable click-through", driver.ignoreMouseCalls)
	}
	state := window.State()
	if state.Mode != PetWindowPassive || !state.ClickThrough {
		t.Fatalf("passive mode state = %#v, want passive click-through state", state)
	}
	if driver.releaseFocusCalls != 1 {
		t.Fatalf("release focus calls = %d, want one best-effort release", driver.releaseFocusCalls)
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

func TestPetWindowPlatformLayerUpdatesZOrderState(t *testing.T) {
	window, driver := newTestPetWindow(t)
	if err := window.Open(); err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	driver.platformLayerTopmost = true
	if err := window.SetPlatformLayer("0x1234"); err != nil {
		t.Fatalf("SetPlatformLayer() error = %v", err)
	}
	if len(driver.platformLayerCalls) != 1 || driver.platformLayerCalls[0] != "0x1234" {
		t.Fatalf("platform layer calls = %v", driver.platformLayerCalls)
	}
	if state := window.State(); !state.AlwaysOnTop {
		t.Fatalf("platform layer state = %#v, want topmost target state", state)
	}

	if err := window.SetPlatformLayer(""); err != nil {
		t.Fatalf("SetPlatformLayer(empty) error = %v", err)
	}
	if state := window.State(); !state.AlwaysOnTop {
		t.Fatalf("ground layer state = %#v, want topmost ground layer", state)
	}
	if err := window.Close(); err != nil {
		t.Fatalf("Close() after platform layer error = %v", err)
	}
	if err := window.Open(); err != nil {
		t.Fatalf("Open() after platform layer error = %v", err)
	}
	if config := driver.openConfigs[len(driver.openConfigs)-1]; !config.AlwaysOnTop {
		t.Fatalf("reopened window config = %#v, want topmost ground layer", config)
	}
}

func TestPetWindowPropagatesActiveDriverOperationErrors(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*fakePetWindowDriver)
		action func(*PetWindow) error
	}{
		{
			name: "ignore mouse",
			setup: func(driver *fakePetWindowDriver) {
				driver.ignoreErr = errors.New("ignore mouse failed")
			},
			action: func(window *PetWindow) error {
				return window.SetMode(PetWindowInteractive)
			},
		},
		{
			name: "position",
			setup: func(driver *fakePetWindowDriver) {
				driver.positionErr = errors.New("position failed")
			},
			action: func(window *PetWindow) error {
				return window.Move(10, 20)
			},
		},
		{
			name: "size",
			setup: func(driver *fakePetWindowDriver) {
				driver.sizeErr = errors.New("size failed")
			},
			action: func(window *PetWindow) error {
				return window.Resize(640, 480)
			},
		},
		{
			name: "always on top",
			setup: func(driver *fakePetWindowDriver) {
				driver.topErr = errors.New("topmost failed")
			},
			action: func(window *PetWindow) error {
				// 桌宠默认处于桌面地面置顶；切换到普通层级才能真正经过 driver，
				// 这样注入的底层错误才是在“活动操作”上生效，而不是被同值幂等短路。
				return window.SetAlwaysOnTop(false)
			},
		},
		{
			name: "platform layer",
			setup: func(driver *fakePetWindowDriver) {
				driver.platformLayerErr = errors.New("platform layer failed")
			},
			action: func(window *PetWindow) error {
				return window.SetPlatformLayer("0x1234")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			window, driver := newTestPetWindow(t)
			if err := window.Open(); err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			tt.setup(driver)
			if err := tt.action(window); err == nil {
				t.Fatal("operation error = nil, want driver error")
			}
		})
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
