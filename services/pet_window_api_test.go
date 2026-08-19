package services

import (
	"errors"
	"runtime"
	"testing"
)

func TestPetWindowAPIForwardsLifecycleAndState(t *testing.T) {
	window, driver := newTestPetWindow(t)
	api := NewPetWindowAPI(window)

	if err := api.Open(); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if state := api.State(); !state.Open || state.Mode != PetWindowPassive {
		t.Fatalf("open state = %#v", state)
	}

	if err := api.Toggle(); err != nil {
		t.Fatalf("Toggle() close error = %v", err)
	}
	if state := api.State(); state.Open {
		t.Fatalf("toggle close state = %#v", state)
	}

	if err := api.Toggle(); err != nil {
		t.Fatalf("Toggle() open error = %v", err)
	}
	if err := api.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if state := api.State(); state.Open || driver.closeCalls != 2 {
		t.Fatalf("close state/calls = %#v/%d", state, driver.closeCalls)
	}
}

func TestPetWindowAPIForwardsWindowControls(t *testing.T) {
	window, driver := newTestPetWindow(t)
	api := NewPetWindowAPI(window)

	if err := api.Open(); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := api.SetMode("interactive"); err != nil {
		t.Fatalf("SetMode(interactive) error = %v", err)
	}
	if err := api.Move(-100, 80); err != nil {
		t.Fatalf("Move() error = %v", err)
	}
	if err := api.Resize(500, 360); err != nil {
		t.Fatalf("Resize() error = %v", err)
	}
	if err := api.Focus(); err != nil {
		t.Fatalf("Focus() error = %v", err)
	}
	if err := api.SetAlwaysOnTop(false); err != nil {
		t.Fatalf("SetAlwaysOnTop() error = %v", err)
	}

	state := api.State()
	if !state.Open || state.Mode != PetWindowInteractive || state.ClickThrough || !state.Focused || state.AlwaysOnTop {
		t.Fatalf("window control state = %#v", state)
	}
	if len(driver.ignoreMouseCalls) != 1 || driver.ignoreMouseCalls[0] {
		t.Fatalf("mode calls = %v", driver.ignoreMouseCalls)
	}
	if len(driver.positionCalls) != 1 || driver.positionCalls[0] != [2]int{-100, 80} {
		t.Fatalf("position calls = %v", driver.positionCalls)
	}
	if len(driver.sizeCalls) != 1 || driver.sizeCalls[0] != [2]int{500, 360} {
		t.Fatalf("size calls = %v", driver.sizeCalls)
	}
	if driver.focusCalls != 1 || len(driver.topCalls) != 1 || driver.topCalls[0] {
		t.Fatalf("focus/top calls = %d/%v", driver.focusCalls, driver.topCalls)
	}
}

func TestPetWindowAPISetModeRejectsInvalidMode(t *testing.T) {
	window, _ := newTestPetWindow(t)
	api := NewPetWindowAPI(window)

	if err := api.SetMode("unsupported"); !errors.Is(err, ErrPetWindowInvalidMode) {
		t.Fatalf("SetMode(unsupported) error = %v, want ErrPetWindowInvalidMode", err)
	}
	if state := api.State(); state.Mode != PetWindowPassive || !state.ClickThrough {
		t.Fatalf("invalid mode state = %#v", state)
	}
}

func TestPetWindowAPINilWindowFailsLifecycleCalls(t *testing.T) {
	api := NewPetWindowAPI(nil)

	for name, action := range map[string]func() error{
		"Open":           api.Open,
		"Close":          api.Close,
		"Toggle":         api.Toggle,
		"SetMode":        func() error { return api.SetMode("interactive") },
		"Move":           func() error { return api.Move(10, 20) },
		"Resize":         func() error { return api.Resize(320, 240) },
		"Focus":          api.Focus,
		"SetAlwaysOnTop": func() error { return api.SetAlwaysOnTop(false) },
	} {
		if err := action(); !errors.Is(err, ErrPetWindowAPIUnavailable) {
			t.Errorf("%s() error = %v, want ErrPetWindowAPIUnavailable", name, err)
		}
	}

	if state := api.State(); state.Open || state.Mode != PetWindowPassive || !state.ClickThrough {
		t.Fatalf("nil API state = %#v", state)
	}
}

func TestPetWindowAPIPropagatesWindowErrors(t *testing.T) {
	window, driver := newTestPetWindow(t)
	api := NewPetWindowAPI(window)
	wantErr := errors.New("open denied")
	driver.openErr = wantErr

	if err := api.Open(); !errors.Is(err, wantErr) {
		t.Fatalf("Open() error = %v, want wrapped driver error", err)
	}
}

func TestPetWindowAPIIdleSecondsNilAPI(t *testing.T) {
	api := NewPetWindowAPI(nil)
	seconds, err := api.IdleSeconds()
	if seconds != 0 || !errors.Is(err, ErrPetWindowAPIUnavailable) {
		t.Fatalf("IdleSeconds() = %d, %v; want 0 and ErrPetWindowAPIUnavailable", seconds, err)
	}
}

func TestPetWindowIdleSecondsPlatformContract(t *testing.T) {
	seconds, err := readPetWindowIdleSeconds()
	if seconds < 0 {
		t.Fatalf("readPetWindowIdleSeconds() seconds = %d, want non-negative", seconds)
	}

	if runtime.GOOS == "windows" {
		if err != nil {
			t.Fatalf("readPetWindowIdleSeconds() on Windows error = %v", err)
		}
		return
	}
	if seconds != 0 || !errors.Is(err, ErrPetWindowIdleUnsupported) {
		t.Fatalf("readPetWindowIdleSeconds() on %s = %d, %v; want 0 and ErrPetWindowIdleUnsupported", runtime.GOOS, seconds, err)
	}
}
