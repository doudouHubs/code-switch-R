package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	appSettingsDir      = ".code-switch" // 【修复】修正拼写错误（原为 .codex-swtich）
	appSettingsFile     = "app.json"
	oldSettingsDir      = ".codex-swtich"               // 旧的错误拼写
	migrationMarkerFile = ".migrated-from-codex-swtich" // 迁移标记文件
)

// AppSettings 的预算字段由旧托盘统计功能写入，保留 JSON 兼容性但不再参与托盘菜单运行路径。
type AppSettings struct {
	ShowHeatmap               bool    `json:"show_heatmap"`
	ShowHomeTitle             bool    `json:"show_home_title"`
	BudgetTotal               float64 `json:"budget_total"`
	BudgetUsedAdjustment      float64 `json:"budget_used_adjustment"`
	BudgetCycleEnabled        bool    `json:"budget_cycle_enabled"`
	BudgetCycleMode           string  `json:"budget_cycle_mode"`
	BudgetRefreshTime         string  `json:"budget_refresh_time"`
	BudgetRefreshDay          int     `json:"budget_refresh_day"`
	BudgetShowCountdown       bool    `json:"budget_show_countdown"`
	BudgetShowForecast        bool    `json:"budget_show_forecast"`
	BudgetForecastMethod      string  `json:"budget_forecast_method"`
	BudgetTotalCodex          float64 `json:"budget_total_codex"`
	BudgetUsedAdjustmentCodex float64 `json:"budget_used_adjustment_codex"`
	BudgetCycleEnabledCodex   bool    `json:"budget_cycle_enabled_codex"`
	BudgetCycleModeCodex      string  `json:"budget_cycle_mode_codex"`
	BudgetRefreshTimeCodex    string  `json:"budget_refresh_time_codex"`
	BudgetRefreshDayCodex     int     `json:"budget_refresh_day_codex"`
	BudgetShowCountdownCodex  bool    `json:"budget_show_countdown_codex"`
	BudgetShowForecastCodex   bool    `json:"budget_show_forecast_codex"`
	BudgetForecastMethodCodex string  `json:"budget_forecast_method_codex"`
	AutoStart                 bool    `json:"auto_start"`
	AutoUpdate                bool    `json:"auto_update"`
	EnableSwitchNotify        bool    `json:"enable_switch_notify"`   // 供应商切换通知开关
	EnableRoundRobin          bool    `json:"enable_round_robin"`     // 同 Level 轮询负载均衡开关（默认关闭）
	EnableRequestCapture      bool    `json:"enable_request_capture"` // 代理请求捕获开关
	EnableCodexHook           bool    `json:"enable_codex_hook"`      // 项目管理 Codex Hook 开关
	RequestCaptureDir         string  `json:"request_capture_dir"`    // 请求捕获存储根目录，留空使用默认目录
	// 语音识别选择是应用级配置，和宠物的 TTS/chat 引用分离；空值表示明确未配置。
	SpeechProviderPlatform *string `json:"speech_provider_platform"`
	SpeechProviderID       *string `json:"speech_provider_id"`
	SpeechModelID          string  `json:"speech_model_id"`
}

// CodexHookRuntimeApplier 让设置服务只依赖 Hook 生命周期能力，避免直接耦合项目管理实现。
// 应用设置落盘前必须先应用运行时状态，失败时不能留下“配置显示关闭、Hook 仍在运行”的半状态。
type CodexHookRuntimeApplier interface {
	ApplyCodexHookEnabled(enabled bool) error
}

// PetSpeechProviderSelection 是语音输入解析器读取的无凭据选择快照。
// provider 实体和 API Key 仍由 ProviderService 事实源解析，不能从设置接口直接带出。
type PetSpeechProviderSelection struct {
	Platform   *string `json:"platform,omitempty"`
	ProviderID *string `json:"providerId,omitempty"`
	ModelID    string  `json:"modelId"`
}

type AppSettingsService struct {
	path                    string
	initErr                 error
	mu                      sync.Mutex
	autoStartService        *AutoStartService
	codexHookRuntimeApplier CodexHookRuntimeApplier
}

func NewAppSettingsService(autoStartService *AutoStartService, runtimeAppliers ...CodexHookRuntimeApplier) *AppSettingsService {
	var codexHookRuntimeApplier CodexHookRuntimeApplier
	if len(runtimeAppliers) > 0 {
		codexHookRuntimeApplier = runtimeAppliers[0]
	}
	home, err := getUserHomeDir()
	if err != nil {
		// 用户目录不可用时不能回退到工作目录，否则便携版或开发命令会把配置写进仓库。
		return &AppSettingsService{
			autoStartService:        autoStartService,
			codexHookRuntimeApplier: codexHookRuntimeApplier,
			initErr:                 err,
		}
	}

	newDir := filepath.Join(home, appSettingsDir)
	newPath := filepath.Join(newDir, appSettingsFile)
	oldDir := filepath.Join(home, oldSettingsDir)
	oldPath := filepath.Join(oldDir, appSettingsFile)
	markerPath := filepath.Join(newDir, migrationMarkerFile)

	// 检查是否已经迁移过
	if _, err := os.Stat(markerPath); os.IsNotExist(err) {
		// 尚未迁移，检查旧目录
		if _, err := os.Stat(oldPath); err == nil {
			// 旧文件存在，执行迁移
			if err := migrateSettings(oldPath, newPath, oldDir, markerPath); err != nil {
				fmt.Printf("[AppSettings] ⚠️  迁移配置失败: %v\n", err)
			}
		}
	}

	return &AppSettingsService{
		path:                    newPath,
		autoStartService:        autoStartService,
		codexHookRuntimeApplier: codexHookRuntimeApplier,
	}
}

// migrateSettings 完整的配置迁移
// 迁移顺序：写新文件 → 校验 → 标记 → 删旧
func migrateSettings(oldPath, newPath, oldDir, markerPath string) error {
	// 1. 确保新目录存在
	if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
		return fmt.Errorf("创建新目录失败: %w", err)
	}

	// 2. 检查新文件是否已存在
	if _, err := os.Stat(newPath); err == nil {
		// 新文件已存在，不覆盖，但仍创建迁移标记
		fmt.Printf("[AppSettings] 新配置文件已存在，跳过迁移\n")
	} else {
		// 3. 读取旧配置
		data, err := os.ReadFile(oldPath)
		if err != nil {
			return fmt.Errorf("读取旧配置失败: %w", err)
		}

		// 4. 写入新配置
		if err := os.WriteFile(newPath, data, 0644); err != nil {
			return fmt.Errorf("写入新配置失败: %w", err)
		}

		// 5. 校验新文件
		verifyData, err := os.ReadFile(newPath)
		if err != nil {
			// 写入成功但读取失败，回滚
			os.Remove(newPath)
			return fmt.Errorf("校验新配置失败（已回滚）: %w", err)
		}

		// 校验内容一致性
		if !bytes.Equal(data, verifyData) {
			os.Remove(newPath)
			return fmt.Errorf("配置内容校验失败（已回滚）: 写入内容与读取内容不一致")
		}

		// 如果是 JSON 文件，额外校验 JSON 格式有效性
		var jsonTest interface{}
		if err := json.Unmarshal(verifyData, &jsonTest); err != nil {
			os.Remove(newPath)
			return fmt.Errorf("JSON 格式校验失败（已回滚）: %w", err)
		}

		fmt.Printf("[AppSettings] ✅ 已迁移并校验配置: %s → %s\n", oldPath, newPath)
	}

	// 6. 创建迁移标记文件
	markerContent := fmt.Sprintf("迁移时间: %s\n旧路径: %s\n", time.Now().Format(time.RFC3339), oldDir)
	if err := os.WriteFile(markerPath, []byte(markerContent), 0644); err != nil {
		return fmt.Errorf("创建迁移标记失败: %w", err)
	}

	// 7. 只有在新文件校验通过后才删除旧目录
	if err := os.RemoveAll(oldDir); err != nil {
		// 删除失败不是致命错误，只记录警告
		fmt.Printf("[AppSettings] ⚠️  删除旧目录失败: %v（可手动删除 %s）\n", err, oldDir)
	} else {
		fmt.Printf("[AppSettings] ✅ 已删除旧目录: %s\n", oldDir)
	}

	return nil
}

func (as *AppSettingsService) defaultSettings() AppSettings {
	// 检查当前开机自启动状态
	autoStartEnabled := false
	if as.autoStartService != nil {
		if enabled, err := as.autoStartService.IsEnabled(); err == nil {
			autoStartEnabled = enabled
		}
	}

	return AppSettings{
		ShowHeatmap:               true,
		ShowHomeTitle:             true,
		BudgetTotal:               0,
		BudgetUsedAdjustment:      0,
		BudgetCycleEnabled:        false,
		BudgetCycleMode:           "daily",
		BudgetRefreshTime:         "00:00",
		BudgetRefreshDay:          1,
		BudgetShowCountdown:       false,
		BudgetShowForecast:        false,
		BudgetForecastMethod:      "cycle",
		BudgetTotalCodex:          0,
		BudgetUsedAdjustmentCodex: 0,
		BudgetCycleEnabledCodex:   false,
		BudgetCycleModeCodex:      "daily",
		BudgetRefreshTimeCodex:    "00:00",
		BudgetRefreshDayCodex:     1,
		BudgetShowCountdownCodex:  false,
		BudgetShowForecastCodex:   false,
		BudgetForecastMethodCodex: "cycle",
		AutoStart:                 autoStartEnabled,
		AutoUpdate:                true,  // 默认开启自动更新
		EnableSwitchNotify:        true,  // 默认开启切换通知
		EnableRoundRobin:          false, // 默认关闭轮询（使用顺序降级）
		EnableRequestCapture:      true,  // 默认开启请求捕获
		EnableCodexHook:           true,  // 默认开启 Codex Hook，保持升级前的行为
		RequestCaptureDir:         "",
		SpeechProviderPlatform:    nil,
		SpeechProviderID:          nil,
		SpeechModelID:             "",
	}
}

// GetAppSettings returns the persisted app settings or defaults if the file does not exist.
func (as *AppSettingsService) GetAppSettings() (AppSettings, error) {
	if err := as.requirePath(); err != nil {
		return AppSettings{}, err
	}
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.loadLocked()
}

// SaveAppSettings persists the provided settings to disk.
func (as *AppSettingsService) SaveAppSettings(settings AppSettings) (AppSettings, error) {
	if err := as.requirePath(); err != nil {
		return settings, err
	}
	as.mu.Lock()
	defer as.mu.Unlock()

	// 旧版本配置没有该字段，loadLocked 会从默认值补成 true；这保证升级后不意外关闭已有监控。
	previousHookEnabled := true
	if previous, loadErr := as.loadLocked(); loadErr == nil {
		previousHookEnabled = previous.EnableCodexHook
	}
	hookChanged := previousHookEnabled != settings.EnableCodexHook
	hookApplier := as.codexHookRuntimeApplier

	normalizedCaptureDir, err := NormalizeRequestCaptureDirForSettings(settings.RequestCaptureDir)
	if err != nil {
		return settings, err
	}
	settings.RequestCaptureDir = normalizedCaptureDir
	settings = normalizeSpeechProviderSettings(settings)

	// 同步开机自启动状态
	if as.autoStartService != nil {
		if settings.AutoStart {
			if err := as.autoStartService.Enable(); err != nil {
				return settings, err
			}
		} else {
			if err := as.autoStartService.Disable(); err != nil {
				return settings, err
			}
		}
	}

	if hookChanged && hookApplier != nil {
		if err := hookApplier.ApplyCodexHookEnabled(settings.EnableCodexHook); err != nil {
			return settings, fmt.Errorf("应用 Codex Hook 设置失败: %w", err)
		}
	}

	if err := as.saveLocked(settings); err != nil {
		if hookChanged && hookApplier != nil {
			// 配置文件写入失败时尽力恢复运行时状态，避免下次启动读取到旧配置却继续使用新状态。
			if rollbackErr := hookApplier.ApplyCodexHookEnabled(previousHookEnabled); rollbackErr != nil {
				fmt.Printf("[AppSettings] Codex Hook 状态回滚失败: %v\n", rollbackErr)
			}
		}
		return settings, err
	}
	return settings, nil
}

func (as *AppSettingsService) requirePath() error {
	if as == nil {
		return fmt.Errorf("应用设置服务未初始化")
	}
	if as.initErr != nil {
		return fmt.Errorf("应用设置目录不可用: %w", as.initErr)
	}
	if as.path == "" || !filepath.IsAbs(as.path) {
		return fmt.Errorf("应用设置路径无效: %q", as.path)
	}
	return nil
}

func (as *AppSettingsService) loadLocked() (AppSettings, error) {
	settings := as.defaultSettings()
	data, err := os.ReadFile(as.path)
	if err != nil {
		if os.IsNotExist(err) {
			return settings, nil
		}
		return settings, err
	}
	if len(data) == 0 {
		return settings, nil
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return settings, err
	}
	if settings.RequestCaptureDir != "" {
		if normalizedDir, err := NormalizeRequestCaptureDirForSettings(settings.RequestCaptureDir); err == nil {
			settings.RequestCaptureDir = normalizedDir
		}
	}
	settings = normalizeSpeechProviderSettings(settings)
	return settings, nil
}

func (as *AppSettingsService) saveLocked(settings AppSettings) error {
	dir := filepath.Dir(as.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(as.path, data, 0o644)
}

// GetSpeechProviderSelection 返回应用级语音识别选择；未配置时返回空引用，
// 调用方必须把它当成“不可用”处理，禁止回退到聊天或 TTS provider。
func (as *AppSettingsService) GetSpeechProviderSelection() (PetSpeechProviderSelection, error) {
	settings, err := as.GetAppSettings()
	if err != nil {
		return PetSpeechProviderSelection{}, err
	}
	return PetSpeechProviderSelection{
		Platform:   cloneOptionalSetting(settings.SpeechProviderPlatform),
		ProviderID: cloneOptionalSetting(settings.SpeechProviderID),
		ModelID:    strings.TrimSpace(settings.SpeechModelID),
	}, nil
}

// SaveSpeechProviderSelection 只更新 speech 三元组，先读取完整设置再保存，
// 避免设置页保存语音时覆盖预算、抓包等不相关配置。
func (as *AppSettingsService) SaveSpeechProviderSelection(selection PetSpeechProviderSelection) (PetSpeechProviderSelection, error) {
	settings, err := as.GetAppSettings()
	if err != nil {
		return PetSpeechProviderSelection{}, err
	}
	settings.SpeechProviderPlatform = cloneOptionalSetting(selection.Platform)
	settings.SpeechProviderID = cloneOptionalSetting(selection.ProviderID)
	settings.SpeechModelID = strings.TrimSpace(selection.ModelID)
	settings = normalizeSpeechProviderSettings(settings)
	if _, err := as.SaveAppSettings(settings); err != nil {
		return PetSpeechProviderSelection{}, err
	}
	return PetSpeechProviderSelection{
		Platform:   cloneOptionalSetting(settings.SpeechProviderPlatform),
		ProviderID: cloneOptionalSetting(settings.SpeechProviderID),
		ModelID:    settings.SpeechModelID,
	}, nil
}

func normalizeSpeechProviderSettings(settings AppSettings) AppSettings {
	settings.SpeechProviderPlatform = cloneOptionalSetting(settings.SpeechProviderPlatform)
	settings.SpeechProviderID = cloneOptionalSetting(settings.SpeechProviderID)
	settings.SpeechModelID = strings.TrimSpace(settings.SpeechModelID)
	return settings
}

func cloneOptionalSetting(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
