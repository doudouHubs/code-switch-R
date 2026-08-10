package services

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	_ "modernc.org/sqlite" // SQLite driver
)

// stringMap 是一个宽容的 map[string]string 类型
// 在 JSON 反序列化时自动将数字、布尔等类型转为字符串
// 用于兼容旧版 cc-switch 配置中 env 值为数字的情况
type stringMap map[string]string

func (m *stringMap) UnmarshalJSON(data []byte) error {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		switch t := v.(type) {
		case string:
			out[k] = t
		case float64:
			// JSON 中所有数字都是 float64，智能格式化（整数不带小数点）
			if t == float64(int64(t)) {
				out[k] = strconv.FormatInt(int64(t), 10)
			} else {
				out[k] = strconv.FormatFloat(t, 'f', -1, 64)
			}
		case bool:
			out[k] = strconv.FormatBool(t)
		case nil:
			out[k] = ""
		default:
			// 其他复杂类型转为 JSON 字符串
			b, _ := json.Marshal(t)
			out[k] = string(b)
		}
	}
	*m = out
	return nil
}

type ConfigImportStatus struct {
	ConfigExists         bool   `json:"config_exists"`
	ConfigPath           string `json:"config_path,omitempty"`
	PendingProviders     bool   `json:"pending_providers"`
	PendingProviderCount int    `json:"pending_provider_count"`
}

type ConfigImportResult struct {
	Status            ConfigImportStatus `json:"status"`
	ImportedProviders int                `json:"imported_providers"`
}

type ImportService struct {
	providerService *ProviderService
}

func NewImportService(ps *ProviderService) *ImportService {
	return &ImportService{providerService: ps}
}

func (is *ImportService) Start() error { return nil }
func (is *ImportService) Stop() error  { return nil }

// IsFirstRun 检查是否首次使用（用于显示导入提示）
func (is *ImportService) IsFirstRun() bool {
	marker, err := firstRunMarkerPath()
	if err != nil {
		log.Printf("⚠️  cc-switch: 获取首次使用标记路径失败: %v", err)
		return true
	}
	if _, err := os.Stat(marker); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true
		}
		log.Printf("⚠️  cc-switch: 检查首次使用标记失败: %v", err)
		return true
	}
	return false
}

// MarkFirstRunDone 标记首次使用已完成（不再显示导入提示）
func (is *ImportService) MarkFirstRunDone() error {
	marker, err := firstRunMarkerPath()
	if err != nil {
		log.Printf("⚠️  cc-switch: 获取首次使用标记路径失败: %v", err)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0755); err != nil {
		log.Printf("⚠️  cc-switch: 创建首次使用标记目录失败: %v", err)
		return err
	}
	if err := os.WriteFile(marker, []byte("1"), 0644); err != nil {
		log.Printf("⚠️  cc-switch: 写入首次使用标记失败: %v", err)
		return err
	}
	log.Printf("✅ cc-switch: 首次使用标记已创建: %s", marker)
	return nil
}

func (is *ImportService) GetStatus() (ConfigImportStatus, error) {
	status := ConfigImportStatus{}
	// 填充配置文件路径，便于前端展示
	if path, err := ccSwitchConfigPath(); err == nil {
		status.ConfigPath = path
	}
	cfg, exists, err := loadCcSwitchConfig()
	if err != nil {
		return status, err
	}
	status.ConfigExists = exists
	if !exists || cfg == nil {
		return status, nil
	}
	return is.evaluateStatus(cfg)
}

// ImportFromPath 从指定路径导入 cc-switch 配置
func (is *ImportService) ImportFromPath(path string) (ConfigImportResult, error) {
	result := ConfigImportResult{}
	path = strings.TrimSpace(path)
	if path == "" {
		err := errors.New("cc-switch: 导入路径为空")
		log.Printf("⚠️  %v", err)
		return result, err
	}
	path = filepath.Clean(path)
	result.Status.ConfigPath = path

	cfg, exists, err := loadCcSwitchConfigFromPath(path)
	if err != nil {
		return result, err
	}
	result.Status.ConfigExists = exists
	if !exists || cfg == nil {
		return result, nil
	}
	pendingProviders, err := is.pendingProviders(cfg)
	if err != nil {
		return result, err
	}
	addedProviders, err := is.importProviders(cfg, pendingProviders)
	if err != nil {
		return result, err
	}
	result.ImportedProviders = addedProviders

	status, err := is.evaluateStatus(cfg)
	if err != nil {
		return result, err
	}
	status.ConfigPath = path
	result.Status = status
	return result, nil
}

// ImportAll 从默认路径导入 cc-switch 配置
func (is *ImportService) ImportAll() (ConfigImportResult, error) {
	path, err := ccSwitchConfigPath()
	if err != nil {
		return ConfigImportResult{}, err
	}
	return is.ImportFromPath(path)
}

func (is *ImportService) evaluateStatus(cfg *ccSwitchConfig) (ConfigImportStatus, error) {
	status := ConfigImportStatus{ConfigExists: true}
	pendingProviders, err := is.pendingProviders(cfg)
	if err != nil {
		return status, err
	}
	providerCount := len(pendingProviders["claude"]) + len(pendingProviders["codex"])
	status.PendingProviders = providerCount > 0
	status.PendingProviderCount = providerCount
	return status, nil
}

func loadCcSwitchConfig() (*ccSwitchConfig, bool, error) {
	path, err := ccSwitchConfigPath()
	if err != nil {
		log.Printf("⚠️  cc-switch: 获取配置路径失败: %v", err)
		return nil, false, err
	}
	return loadCcSwitchConfigFromPath(path)
}

func loadCcSwitchConfigFromPath(path string) (*ccSwitchConfig, bool, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" {
		err := errors.New("cc-switch: 配置路径为空")
		log.Printf("⚠️  %v", err)
		return nil, false, err
	}

	// 检测是否为 SQLite 文件
	if isSQLiteFile(path) {
		log.Printf("ℹ️  cc-switch: 检测到 SQLite 数据库: %s", path)
		return loadCcSwitchConfigFromSQLite(path)
	}

	// JSON 文件处理（原有逻辑）
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			log.Printf("ℹ️  cc-switch: 配置文件不存在: %s", path)
			return nil, false, nil
		}
		log.Printf("⚠️  cc-switch: 读取配置文件失败: %s - %v", path, err)
		return nil, false, err
	}
	if len(data) == 0 {
		log.Printf("ℹ️  cc-switch: 配置文件为空: %s", path)
		return &ccSwitchConfig{}, true, nil
	}
	var cfg ccSwitchConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("⚠️  cc-switch: JSON 解析失败: %s - %v", path, err)
		return nil, true, err
	}
	log.Printf("✅ cc-switch: 配置文件加载成功: %s", path)
	return &cfg, true, nil
}

// isSQLiteFile 检测文件是否为 SQLite 数据库
// 必须同时满足：文件存在 + 文件头为 SQLite 魔数
func isSQLiteFile(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	// 检查文件头（SQLite 魔数: "SQLite format 3\x00"）
	header := make([]byte, 16)
	n, err := file.Read(header)
	if err != nil || n < 16 {
		return false
	}

	return bytes.HasPrefix(header, []byte("SQLite format 3"))
}

// loadCcSwitchConfigFromSQLite 从 SQLite 数据库加载 cc-switch 配置
func loadCcSwitchConfigFromSQLite(path string) (*ccSwitchConfig, bool, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		log.Printf("⚠️  cc-switch: 打开 SQLite 失败: %v", err)
		return nil, true, err
	}
	defer db.Close()

	cfg := &ccSwitchConfig{
		Claude: ccProviderSection{Providers: map[string]ccProviderEntry{}},
		Codex:  ccProviderSection{Providers: map[string]ccProviderEntry{}},
	}

	// 1. 读取 providers
	if err := loadProvidersFromSQLite(db, cfg); err != nil {
		log.Printf("⚠️  cc-switch: 读取 providers 失败: %v", err)
		return nil, true, err
	}

	log.Printf("✅ cc-switch: SQLite 数据库加载成功: %s", path)
	return cfg, true, nil
}

// loadProvidersFromSQLite 从 SQLite 读取 providers 数据
func loadProvidersFromSQLite(db *sql.DB, cfg *ccSwitchConfig) error {
	// 读取 provider_endpoints 作为 URL 补充
	endpoints := make(map[string]string) // key: "app_type|provider_id" -> url
	epRows, err := db.Query(`SELECT provider_id, app_type, url FROM provider_endpoints`)
	if err == nil {
		defer epRows.Close()
		for epRows.Next() {
			var pid, appType, url string
			if err := epRows.Scan(&pid, &appType, &url); err == nil {
				url = strings.TrimSpace(url)
				if url != "" {
					key := strings.ToLower(appType) + "|" + pid
					endpoints[key] = url
				}
			}
		}
	}

	// 读取 providers
	rows, err := db.Query(`SELECT id, app_type, name, settings_config, COALESCE(website_url, '') FROM providers`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var id, appType, name, settingsJSON, website string
		if err := rows.Scan(&id, &appType, &name, &settingsJSON, &website); err != nil {
			log.Printf("⚠️  cc-switch: 扫描 provider 行失败: %v", err)
			continue
		}

		entry := ccProviderEntry{
			ID:         id,
			Name:       name,
			WebsiteURL: website,
			Settings: ccProviderSetting{
				Env:  stringMap{},
				Auth: stringMap{},
			},
		}

		// 解析 settings_config JSON
		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(settingsJSON), &raw); err == nil {
			// 解析 env
			if env, ok := raw["env"].(map[string]interface{}); ok {
				for k, v := range env {
					entry.Settings.Env[k] = fmt.Sprint(v)
				}
			}
			// 解析 auth
			if auth, ok := raw["auth"].(map[string]interface{}); ok {
				for k, v := range auth {
					entry.Settings.Auth[k] = fmt.Sprint(v)
				}
			}
			// 解析 config (Codex TOML)
			if cfgStr, ok := raw["config"].(string); ok {
				entry.Settings.Config = cfgStr
			}
		}

		// 从 provider_endpoints 补充 URL
		kind := strings.ToLower(strings.TrimSpace(appType))
		if url := endpoints[kind+"|"+id]; url != "" {
			if kind == "claude" {
				// Claude: 补充 ANTHROPIC_BASE_URL
				if entry.Settings.Env["ANTHROPIC_BASE_URL"] == "" {
					entry.Settings.Env["ANTHROPIC_BASE_URL"] = url
				}
			} else if kind == "codex" {
				// Codex: 如果没有 Config，生成最小 TOML
				if entry.Settings.Config == "" {
					entry.Settings.Config = fmt.Sprintf(
						"model_provider = \"db\"\n[model_providers.db]\nbase_url = \"%s\"\nname = \"%s\"",
						url, name,
					)
				}
			}
		}

		// 添加到对应平台
		if kind == "codex" {
			cfg.Codex.Providers[id] = entry
		} else {
			cfg.Claude.Providers[id] = entry
		}
	}

	return rows.Err()
}

func ccSwitchConfigPath() (string, error) {
	home, err := getUserHomeDir()
	if err != nil {
		return "", err
	}
	// 优先检查 SQLite 数据库（新版 cc-switch），然后是 JSON 配置文件
	candidates := []string{
		filepath.Join(home, ".cc-switch", "cc-switch.db"),         // 新版 SQLite
		filepath.Join(home, ".cc-switch", "config.json.migrated"), // 旧版迁移后的 JSON
		filepath.Join(home, ".cc-switch", "config.json"),          // 旧版 JSON
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err // 权限/IO 等异常立即暴露
		}
	}
	// 未找到现有文件时，默认使用 SQLite 路径
	return candidates[0], nil
}

func firstRunMarkerPath() (string, error) {
	home, err := getUserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".code-switch", ".import_prompted"), nil
}

type ccSwitchConfig struct {
	Claude ccProviderSection `json:"claude"`
	Codex  ccProviderSection `json:"codex"`
}

type ccProviderSection struct {
	Providers map[string]ccProviderEntry `json:"providers"`
}

type ccProviderEntry struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	WebsiteURL string            `json:"websiteUrl"`
	Settings   ccProviderSetting `json:"settingsConfig"`
}

type ccProviderSetting struct {
	Env    stringMap `json:"env"`  // 使用 stringMap 兼容旧配置中数字类型的值
	Auth   stringMap `json:"auth"` // 使用 stringMap 兼容旧配置中数字类型的值
	Config string    `json:"config"`
}

type providerCandidate struct {
	Name   string
	APIURL string
	APIKey string
	Site   string
	Icon   string
}

func (is *ImportService) pendingProviders(cfg *ccSwitchConfig) (map[string][]providerCandidate, error) {
	result := map[string][]providerCandidate{
		"claude": {},
		"codex":  {},
	}
	claudeExisting, err := is.providerService.LoadProviders("claude")
	if err != nil {
		return nil, err
	}
	codexExisting, err := is.providerService.LoadProviders("codex")
	if err != nil {
		return nil, err
	}
	result["claude"] = diffProviderCandidates("claude", cfg.Claude.Providers, claudeExisting)
	result["codex"] = diffProviderCandidates("codex", cfg.Codex.Providers, codexExisting)
	return result, nil
}

func diffProviderCandidates(kind string, entries map[string]ccProviderEntry, existing []Provider) []providerCandidate {
	if len(entries) == 0 {
		return []providerCandidate{}
	}
	existingURL := make(map[string]struct{})
	existingNames := make(map[string]struct{})
	for _, provider := range existing {
		if url := normalizeURL(provider.APIURL); url != "" {
			existingURL[url] = struct{}{}
		}
		if name := normalizeName(provider.Name); name != "" {
			existingNames[name] = struct{}{}
		}
	}
	seen := make(map[string]struct{})
	candidates := make([]providerCandidate, 0, len(entries))
	for key, entry := range entries {
		candidate, ok := parseProviderEntry(kind, key, entry)
		if !ok {
			continue
		}
		if url := normalizeURL(candidate.APIURL); url != "" {
			if _, exists := existingURL[url]; exists {
				continue
			}
			if _, dup := seen[url]; dup {
				continue
			}
		}
		if name := normalizeName(candidate.Name); name != "" {
			if _, exists := existingNames[name]; exists {
				continue
			}
		}
		dedupKey := normalizeURL(candidate.APIURL)
		if dedupKey == "" {
			dedupKey = normalizeName(candidate.Name)
		}
		if dedupKey != "" {
			seen[dedupKey] = struct{}{}
		}
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return strings.ToLower(candidates[i].Name) < strings.ToLower(candidates[j].Name)
	})
	return candidates
}

func parseProviderEntry(kind, key string, entry ccProviderEntry) (providerCandidate, bool) {
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		name = strings.TrimSpace(entry.ID)
	}
	if name == "" {
		name = strings.TrimSpace(key)
	}
	site := strings.TrimSpace(entry.WebsiteURL)
	switch strings.ToLower(kind) {
	case "claude":
		apiURL := strings.TrimSpace(entry.Settings.Env["ANTHROPIC_BASE_URL"])
		apiKey := strings.TrimSpace(entry.Settings.Env["ANTHROPIC_AUTH_TOKEN"])
		if apiURL == "" || apiKey == "" {
			log.Printf("ℹ️  cc-switch: 跳过 claude provider [%s]: 缺少 ANTHROPIC_BASE_URL 或 ANTHROPIC_AUTH_TOKEN", key)
			return providerCandidate{}, false
		}
		return providerCandidate{Name: name, APIURL: apiURL, APIKey: apiKey, Site: site}, true
	case "codex":
		apiKey := pickFirstNonEmpty(
			entry.Settings.Auth["OPENAI_API_KEY"],
			entry.Settings.Auth["OPENAI_API_KEY_1"],
			entry.Settings.Auth["OPENAI_API_KEY_V2"],
			entry.Settings.Env["OPENAI_API_KEY"],
		)
		if apiKey == "" {
			log.Printf("ℹ️  cc-switch: 跳过 codex provider [%s]: 缺少 OPENAI_API_KEY", key)
			return providerCandidate{}, false
		}
		apiURL := resolveCodexAPIURL(entry.Settings.Config)
		if apiURL == "" {
			log.Printf("ℹ️  cc-switch: 跳过 codex provider [%s]: 无法解析 API URL (TOML 配置无效或缺失)", key)
			return providerCandidate{}, false
		}
		return providerCandidate{Name: name, APIURL: apiURL, APIKey: apiKey, Site: site}, true
	default:
		return providerCandidate{}, false
	}
}

type ccImportCodexConfig struct {
	ModelProvider    string                                 `toml:"model_provider"`
	AltModelProvider string                                 `toml:"nmodel_provider"`
	Providers        map[string]ccImportCodexProviderConfig `toml:"model_providers"`
}

type ccImportCodexProviderConfig struct {
	Name    string `toml:"name"`
	BaseURL string `toml:"base_url"`
}

func resolveCodexAPIURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var cfg ccImportCodexConfig
	if err := toml.Unmarshal([]byte(raw), &cfg); err != nil {
		return ""
	}
	providerKey := cfg.ModelProvider
	if providerKey == "" {
		providerKey = cfg.AltModelProvider
	}
	if providerKey != "" {
		if provider, ok := cfg.Providers[providerKey]; ok {
			return strings.TrimSpace(provider.BaseURL)
		}
		lower := strings.ToLower(providerKey)
		for key, provider := range cfg.Providers {
			if strings.ToLower(key) == lower {
				return strings.TrimSpace(provider.BaseURL)
			}
			if strings.ToLower(strings.TrimSpace(provider.Name)) == lower {
				return strings.TrimSpace(provider.BaseURL)
			}
		}
	}
	for _, provider := range cfg.Providers {
		if url := strings.TrimSpace(provider.BaseURL); url != "" {
			return url
		}
	}
	return ""
}

func pickFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func normalizeURL(value string) string {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimRight(trimmed, "/")
	return strings.ToLower(trimmed)
}

func normalizeName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func (is *ImportService) importProviders(cfg *ccSwitchConfig, pending map[string][]providerCandidate) (int, error) {
	total := 0
	if candidates := pending["claude"]; len(candidates) > 0 {
		added, err := is.saveProviders("claude", candidates)
		if err != nil {
			return total, err
		}
		total += added
	}
	if candidates := pending["codex"]; len(candidates) > 0 {
		added, err := is.saveProviders("codex", candidates)
		if err != nil {
			return total, err
		}
		total += added
	}
	return total, nil
}

func (is *ImportService) saveProviders(kind string, candidates []providerCandidate) (int, error) {
	existing, err := is.providerService.LoadProviders(kind)
	if err != nil {
		return 0, err
	}
	nextID := nextProviderID(existing)
	merged := make([]Provider, 0, len(existing)+len(candidates))
	merged = append(merged, existing...)
	accent, tint := defaultVisual(kind)
	for _, candidate := range candidates {
		provider := Provider{
			ID:      nextID,
			Name:    candidate.Name,
			APIURL:  candidate.APIURL,
			APIKey:  candidate.APIKey,
			Site:    candidate.Site,
			Icon:    candidate.Icon,
			Tint:    tint,
			Accent:  accent,
			Enabled: true,
		}
		merged = append(merged, provider)
		nextID++
	}
	if err := is.providerService.SaveProviders(kind, merged); err != nil {
		return 0, err
	}
	return len(candidates), nil
}

func nextProviderID(list []Provider) int64 {
	maxID := int64(0)
	for _, provider := range list {
		if provider.ID > maxID {
			maxID = provider.ID
		}
	}
	return maxID + 1
}

func defaultVisual(kind string) (accent, tint string) {
	switch strings.ToLower(kind) {
	case "codex":
		return "#ec4899", "rgba(236, 72, 153, 0.16)"
	default:
		return "#0a84ff", "rgba(15, 23, 42, 0.12)"
	}
}
