package services

// PetWindowPlatformRect 使用屏幕物理像素描述窗口几何。
// Windows 原生 API 返回物理坐标，前端会结合 Overlay 做一次 CSS/DIP 换算，
// 不能在 Go 层提前假设 WebView 的 DPI 缩放比例。
type PetWindowPlatformRect struct {
	Left   int32 `json:"left"`
	Top    int32 `json:"top"`
	Right  int32 `json:"right"`
	Bottom int32 `json:"bottom"`
}

// PetWindowPlatform 表示一个可供桌宠站立的外部顶层窗口。
// ID 使用字符串承载 HWND，避免 JavaScript number 在 64 位句柄上发生精度损失。
type PetWindowPlatform struct {
	ID     string                `json:"id"`
	Rect   PetWindowPlatformRect `json:"rect"`
	ZOrder int                   `json:"zOrder"`
}

// PetWindowPlatformSnapshot 是一次平台查询的完整几何快照。
// Overlay 让前端可以把屏幕物理像素映射到透明桌宠窗口的 CSS 坐标。
type PetWindowPlatformSnapshot struct {
	Available      bool                  `json:"available"`
	Overlay        PetWindowPlatformRect `json:"overlay"`
	Platforms      []PetWindowPlatform   `json:"platforms"`
	Occluders      []PetWindowPlatform   `json:"occluders"`
	MovingWindowID string                `json:"movingWindowId,omitempty"`
}

type petWindowPlatformReader interface {
	GetPlatforms() (PetWindowPlatformSnapshot, error)
}

// GetPlatforms 获取当前桌宠可用的平台快照。平台是运行时能力，不参与宠物配置持久化。
func (w *PetWindow) GetPlatforms() (PetWindowPlatformSnapshot, error) {
	if w == nil {
		return PetWindowPlatformSnapshot{}, ErrPetWindowAPIUnavailable
	}

	w.mu.Lock()
	driver := w.driver
	open := w.open
	w.mu.Unlock()
	if !open {
		// 桌宠窗口尚未显示时没有可信的 overlay 原点；前端应继续使用桌面地面。
		return PetWindowPlatformSnapshot{}, nil
	}

	reader, ok := driver.(petWindowPlatformReader)
	if !ok {
		return PetWindowPlatformSnapshot{}, nil
	}
	return reader.GetPlatforms()
}
