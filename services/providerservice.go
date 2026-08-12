package services

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Provider struct {
	ID      int64  `json:"id"` // 修复：使用 int64 支持大 ID 值
	Name    string `json:"name"`
	APIURL  string `json:"apiUrl"`
	APIKey  string `json:"apiKey"`
	Site    string `json:"officialSite"`
	Icon    string `json:"icon"`
	Tint    string `json:"tint"`
	Accent  string `json:"accent"`
	Enabled bool   `json:"enabled"`

	// API 端点路径（可选）- 覆盖平台默认端点
	// 如：GLM 模型需要使用 /v1/chat/completions 而非 /v1/messages
	// 留空则使用平台默认（claude: /v1/messages, codex: /responses）
	APIEndpoint string `json:"apiEndpoint,omitempty"`

	// 模型白名单 - Provider 原生支持的模型名
	// 使用 map 实现 O(1) 查找，向后兼容（omitempty）
	SupportedModels map[string]bool `json:"supportedModels,omitempty"`

	// 模型映射 - 外部模型名 -> Provider 内部模型名
	// 支持精确匹配和通配符（如 "claude-*" -> "anthropic/claude-*"）
	ModelMapping map[string]string `json:"modelMapping,omitempty"`

	// 模型用途类别 - 模型名（或通配符）-> chat/speech/embedding/image/video。
	// 这是宠物语音等能力校验的显式事实源；空值继续兼容旧 provider 配置。
	ModelCategories map[string]string `json:"modelCategories,omitempty"`

	// 优先级分组 - 数字越小优先级越高（1-10，默认 1）
	// 使用 omitempty 确保零值不序列化，向后兼容
	Level int `json:"level,omitempty"`

	// 认证方式 - bearer / x-api-key / 自定义 Header 名
	// 空值时使用平台默认（claude: x-api-key, codex: bearer）
	ConnectivityAuthType string `json:"connectivityAuthType,omitempty"`

	// 上游协议类型 - anthropic / openai_chat / auto
	// anthropic: 上游使用 Anthropic Messages API（默认）
	// openai_chat: 上游使用 OpenAI Chat Completions API，自动转换请求/响应格式
	// auto: 根据 APIEndpoint 自动检测（包含 /chat/completions 则为 openai_chat）
	UpstreamProtocol string `json:"upstreamProtocol,omitempty"`

	// 内部字段：配置验证错误（不持久化）
	configErrors []string `json:"-"`
}

type providerEnvelope struct {
	Providers []Provider `json:"providers"`
}

type ProviderService struct {
	mu sync.Mutex
}

func NewProviderService() *ProviderService {
	return &ProviderService{}
}

func (ps *ProviderService) Start() error { return nil }
func (ps *ProviderService) Stop() error  { return nil }

func providerFilePath(kind string) (string, error) {
	home, err := getUserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".code-switch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var filename string
	switch strings.ToLower(kind) {
	case "claude", "claude-code", "claude_code":
		filename = "claude-code.json"
	case "codex":
		filename = "codex.json"
	default:
		// 支持自定义 CLI 工具的供应商存储：custom:{tool-id}
		if strings.HasPrefix(kind, "custom:") {
			toolId := strings.TrimPrefix(kind, "custom:")
			if toolId == "" {
				return "", fmt.Errorf("invalid custom provider kind: %s", kind)
			}
			// 存储在 providers 子目录下
			providersDir := filepath.Join(dir, "providers")
			if err := os.MkdirAll(providersDir, 0o755); err != nil {
				return "", err
			}
			return filepath.Join(providersDir, toolId+".json"), nil
		}
		return "", fmt.Errorf("unknown provider type: %s", kind)
	}
	return filepath.Join(dir, filename), nil
}

func (ps *ProviderService) SaveProviders(kind string, providers []Provider) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.saveProvidersLocked(kind, providers)
}

// loadProvidersRaw 原样读取配置文件（不迁移、不保存）
// 用于内部需要读取现有配置但不触发迁移的场景（如名称校验）
func (ps *ProviderService) loadProvidersRaw(kind string) ([]Provider, error) {
	path, err := providerFilePath(kind)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var envelope providerEnvelope
	if len(data) == 0 {
		return []Provider{}, nil
	}

	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}

	return envelope.Providers, nil
}

// saveProvidersLocked 内部保存方法，调用方必须已持有锁
func (ps *ProviderService) saveProvidersLocked(kind string, providers []Provider) error {
	path, err := providerFilePath(kind)
	if err != nil {
		return err
	}
	providers = sanitizeProvidersForKind(kind, providers)

	// 加载现有配置，用于检查 name 是否被修改
	// 使用原样读取，避免触发迁移导致死锁
	existingProviders, err := ps.loadProvidersRaw(kind)
	if err != nil {
		return err
	}
	nameByID := make(map[int64]string, len(existingProviders))
	for _, p := range existingProviders {
		nameByID[p.ID] = p.Name
	}

	// 解析 platform(alias 校验需要)。失败时跳过 alias 校验(比如 gemini/unknown kind)
	aliasPlatform, aliasErr := resolvePlatform(kind)

	// 验证每个 provider 的配置，并清除旧字段
	validationErrors := make([]string, 0)
	for i := range providers {
		p := &providers[i]

		// 规则：name 不可修改（走独立 RenameProvider 路径,SaveProviders 只允许既有 name）
		if oldName, ok := nameByID[p.ID]; ok && oldName != p.Name {
			return fmt.Errorf("provider id %d 的 name 不可修改(请使用 RenameProvider)", p.ID)
		}

		// 规则:名字不得占用其他 provider 的 48h 活动 alias
		// 防止 "A→B 后新建同名 A" 被 alias resolver 静默归并到 B 的历史里
		if aliasErr == nil {
			if err := checkNameNotOccupiedByAlias(aliasPlatform, p.ID, p.Name); err != nil {
				return err
			}
		}

		// 验证模型配置
		if errs := p.ValidateConfiguration(); len(errs) > 0 {
			for _, errMsg := range errs {
				validationErrors = append(validationErrors, fmt.Sprintf("[%s] %s", p.Name, errMsg))
			}
		}

	}

	// 如果有验证错误，返回汇总错误
	if len(validationErrors) > 0 {
		return fmt.Errorf("配置验证失败：\n  - %s", strings.Join(validationErrors, "\n  - "))
	}

	data, err := json.MarshalIndent(providerEnvelope{Providers: providers}, "", "  ")
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (ps *ProviderService) LoadProviders(kind string) ([]Provider, error) {
	path, err := providerFilePath(kind)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var envelope providerEnvelope
	if len(data) == 0 {
		return []Provider{}, nil
	}

	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	envelope.Providers = sanitizeProvidersForKind(kind, envelope.Providers)

	return envelope.Providers, nil
}

// sanitizeProvidersForKind 清理 provider 列表中的非真实业务数据。
// 当前只处理 Claude：历史调试/测试流程可能把名为 "B" 的空壳 provider 混入配置。
// 过滤必须放在 ProviderService 这个事实源边界，而不是前端页面里打补丁；
// 这样代理、可用性检查、端点同步等所有读取方都会拿到同一份真实数据。
func sanitizeProvidersForKind(kind string, providers []Provider) []Provider {
	if !isClaudeProviderKind(kind) || len(providers) == 0 {
		return providers
	}

	filtered := make([]Provider, 0, len(providers))
	changed := false
	for _, provider := range providers {
		if isPlaceholderClaudeBProvider(provider) {
			changed = true
			continue
		}
		filtered = append(filtered, provider)
	}
	if !changed {
		return providers
	}
	return filtered
}

func isClaudeProviderKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "claude", "claude-code", "claude_code":
		return true
	default:
		return false
	}
}

func isPlaceholderClaudeBProvider(provider Provider) bool {
	if !strings.EqualFold(strings.TrimSpace(provider.Name), "B") {
		return false
	}

	// 只删除缺少真实连接信息的 B，避免误删用户主动创建的真实 B 供应商。
	// icon/tint/accent 这类展示字段不能证明 provider 可用；apiUrl/apiKey/officialSite 才是业务数据。
	apiURL := strings.TrimSpace(provider.APIURL)
	return (apiURL == "" || strings.EqualFold(apiURL, "u")) &&
		strings.TrimSpace(provider.APIKey) == "" &&
		strings.TrimSpace(provider.Site) == ""
}

// loadProvidersNoLock 内部加载方法，在持有锁的情况下调用（避免递归加锁）
// 仅在已持有 ps.mu 锁的上下文中调用（如 DuplicateProvider）
func (ps *ProviderService) loadProvidersNoLock(kind string) ([]Provider, error) {
	path, err := providerFilePath(kind)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var envelope providerEnvelope
	if len(data) == 0 {
		return []Provider{}, nil
	}

	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}

	return envelope.Providers, nil
}

// DuplicateProvider 复制供应商配置，生成新的副本
// 返回新创建的 Provider 对象
func (ps *ProviderService) DuplicateProvider(kind string, sourceID int64) (*Provider, error) {
	// 1. 先加锁，避免并发修改
	ps.mu.Lock()
	defer ps.mu.Unlock()

	// 2. 加载现有配置（在锁内完成，确保数据一致性）
	// 注意：LoadProviders 内部可能触发迁移保存，会再次尝试加锁导致死锁
	// 因此使用不加锁的内部加载逻辑
	providers, err := ps.loadProvidersNoLock(kind)
	if err != nil {
		return nil, fmt.Errorf("加载供应商配置失败: %w", err)
	}

	// 3. 查找源供应商
	var source *Provider
	for i := range providers {
		if providers[i].ID == sourceID {
			source = &providers[i]
			break
		}
	}
	if source == nil {
		return nil, fmt.Errorf("未找到 ID 为 %d 的供应商", sourceID)
	}

	// 4. 生成新 ID（当前最大 ID + 1）
	maxID := int64(0)
	for _, p := range providers {
		if p.ID > maxID {
			maxID = p.ID
		}
	}
	newID := maxID + 1

	// 5. 克隆配置（深拷贝）
	cloned := &Provider{
		ID:                   newID,
		Name:                 source.Name + " (副本)",
		APIURL:               source.APIURL,
		APIKey:               source.APIKey,
		Site:                 source.Site,
		Icon:                 source.Icon,
		Tint:                 source.Tint,
		Accent:               source.Accent,
		Enabled:              false, // 默认禁用，避免与源供应商冲突
		Level:                source.Level,
		APIEndpoint:          source.APIEndpoint,          // 复制端点配置
		UpstreamProtocol:     source.UpstreamProtocol,     // 复制上游协议配置
		ConnectivityAuthType: source.ConnectivityAuthType, // 复制认证方式
	}

	// 6. 深拷贝 map（避免共享引用）
	if source.SupportedModels != nil {
		cloned.SupportedModels = make(map[string]bool, len(source.SupportedModels))
		for k, v := range source.SupportedModels {
			cloned.SupportedModels[k] = v
		}
	}

	if source.ModelMapping != nil {
		cloned.ModelMapping = make(map[string]string, len(source.ModelMapping))
		for k, v := range source.ModelMapping {
			cloned.ModelMapping[k] = v
		}
	}

	if source.ModelCategories != nil {
		cloned.ModelCategories = make(map[string]string, len(source.ModelCategories))
		for k, v := range source.ModelCategories {
			cloned.ModelCategories[k] = v
		}
	}

	// 7. 添加到列表并保存（使用内部方法避免死锁）
	providers = append(providers, *cloned)
	if err := ps.saveProvidersLocked(kind, providers); err != nil {
		return nil, fmt.Errorf("保存副本失败: %w", err)
	}

	return cloned, nil
}

// IsModelSupported 检查 provider 是否支持指定的模型
// 支持条件：1) 模型在 SupportedModels 中（精确或通配符匹配）
//  2. 模型在 ModelMapping 的 key 中（精确或通配符匹配）
func (p *Provider) IsModelSupported(modelName string) bool {
	// 向后兼容：如果未配置白名单和映射，假设支持所有模型
	if (p.SupportedModels == nil || len(p.SupportedModels) == 0) &&
		(p.ModelMapping == nil || len(p.ModelMapping) == 0) {
		return true
	}

	// 场景 A：Provider 原生支持该模型（精确匹配）
	if p.SupportedModels != nil && p.SupportedModels[modelName] {
		return true
	}

	// 场景 A+：Provider 原生支持该模型（通配符匹配）
	if p.SupportedModels != nil {
		for supportedModel := range p.SupportedModels {
			if matchWildcard(supportedModel, modelName) {
				return true
			}
		}
	}

	// 场景 B：Provider 通过映射支持该模型（精确匹配）
	if p.ModelMapping != nil {
		if _, exists := p.ModelMapping[modelName]; exists {
			return true
		}

		// 场景 B+：通过通配符映射支持
		for pattern := range p.ModelMapping {
			if matchWildcard(pattern, modelName) {
				return true
			}
		}
	}

	// 场景 C：不支持
	return false
}

// GetEffectiveModel 获取实际应该使用的模型名
// 如果存在映射（精确或通配符），返回映射后的模型名；否则返回原模型名
func (p *Provider) GetEffectiveModel(requestedModel string) string {
	if p.ModelMapping == nil || len(p.ModelMapping) == 0 {
		return requestedModel
	}

	// 优先查找精确映射
	if mappedModel, exists := p.ModelMapping[requestedModel]; exists {
		return mappedModel
	}

	// 查找通配符映射
	for pattern, replacement := range p.ModelMapping {
		if matchWildcard(pattern, requestedModel) {
			return applyWildcardMapping(pattern, replacement, requestedModel)
		}
	}

	// 无映射，返回原模型名
	return requestedModel
}

// GetModelCategory 返回模型的显式用途类别。
// 先查 effective model，再查请求侧别名，确保 ModelMapping 后的真实模型类别优先。
func (p *Provider) GetModelCategory(requestedModel, effectiveModel string) (string, bool) {
	if p == nil || len(p.ModelCategories) == 0 {
		return "", false
	}
	for _, model := range []string{effectiveModel, requestedModel} {
		if category, ok := p.ModelCategories[model]; ok {
			return normalizeModelCategory(category), true
		}
	}
	// Go map 的遍历顺序不稳定；多个通配符同时命中时，优先选择字面量更长的
	// 规则，再用规则文本打破平局，保证同一 provider 在不同进程中得到同一类别。
	matchedPattern := ""
	matchedCategory := ""
	matchedSpecificity := -1
	for pattern, category := range p.ModelCategories {
		if !matchWildcard(pattern, effectiveModel) && !matchWildcard(pattern, requestedModel) {
			continue
		}
		specificity := modelCategoryPatternSpecificity(pattern)
		if specificity > matchedSpecificity || (specificity == matchedSpecificity && (matchedPattern == "" || pattern < matchedPattern)) {
			matchedPattern = pattern
			matchedCategory = category
			matchedSpecificity = specificity
		}
	}
	if matchedPattern != "" {
		return normalizeModelCategory(matchedCategory), true
	}
	// 类别表只约束命中的模型；未命中时返回 false，让旧 provider 继续走
	// 模型名兼容规则，避免新增一条类别配置后误伤历史模型。
	return "", false
}

func modelCategoryPatternSpecificity(pattern string) int {
	return len(strings.ReplaceAll(pattern, "*", ""))
}

// GetEffectiveEndpoint 获取有效的 API 端点
// 优先使用用户配置的端点，否则使用平台默认
func (p *Provider) GetEffectiveEndpoint(defaultEndpoint string) string {
	ep := strings.TrimSpace(p.APIEndpoint)
	if ep == "" {
		return defaultEndpoint
	}

	// 校验：必须是相对路径，不能是完整 URL
	if strings.HasPrefix(ep, "http://") || strings.HasPrefix(ep, "https://") {
		log.Printf("[Provider] 警告: apiEndpoint 应该是相对路径（如 /v1/chat/completions），而非完整 URL: %s，使用默认端点", ep)
		return defaultEndpoint
	}

	// 确保以 / 开头
	if !strings.HasPrefix(ep, "/") {
		ep = "/" + ep
	}

	return ep
}

// UpstreamProtocolType 上游协议类型
type UpstreamProtocolType string

const (
	// UpstreamProtocolAnthropic Anthropic Messages API（默认）
	UpstreamProtocolAnthropic UpstreamProtocolType = "anthropic"
	// UpstreamProtocolOpenAIChat OpenAI Chat Completions API
	UpstreamProtocolOpenAIChat UpstreamProtocolType = "openai_chat"
	// UpstreamProtocolAuto 自动检测
	UpstreamProtocolAuto UpstreamProtocolType = "auto"
)

// GetUpstreamProtocol 获取上游协议类型
// 空值或无效值默认返回 anthropic
func (p *Provider) GetUpstreamProtocol() UpstreamProtocolType {
	protocol := strings.TrimSpace(strings.ToLower(p.UpstreamProtocol))
	switch protocol {
	case "openai_chat", "openai-chat", "openai":
		return UpstreamProtocolOpenAIChat
	case "auto":
		return UpstreamProtocolAuto
	default:
		return UpstreamProtocolAnthropic
	}
}

// DetectUpstreamProtocol 根据端点自动检测上游协议
// 用于 auto 模式的启发式判断
func DetectUpstreamProtocol(endpoint string) UpstreamProtocolType {
	ep := strings.ToLower(endpoint)
	// 检测 OpenAI Chat Completions 端点
	if strings.Contains(ep, "/chat/completions") {
		return UpstreamProtocolOpenAIChat
	}
	// 默认 Anthropic
	return UpstreamProtocolAnthropic
}

// ResolveUpstreamProtocol 解析最终的上游协议
// 如果是 auto 模式，根据端点自动检测
func (p *Provider) ResolveUpstreamProtocol(effectiveEndpoint string) UpstreamProtocolType {
	protocol := p.GetUpstreamProtocol()
	if protocol == UpstreamProtocolAuto {
		return DetectUpstreamProtocol(effectiveEndpoint)
	}
	return protocol
}

// ValidateConfiguration 验证 provider 的模型配置
// 返回验证错误列表（空则表示验证通过）
func (p *Provider) ValidateConfiguration() []string {
	errors := make([]string, 0)

	// 规则 1：ModelMapping 的 value 必须在 SupportedModels 中
	// 仅当两者都有实际内容时才校验（空 map 不触发校验）
	if len(p.ModelMapping) > 0 && len(p.SupportedModels) > 0 {
		for externalModel, internalModel := range p.ModelMapping {
			// 检查是否为通配符映射
			if strings.Contains(internalModel, "*") {
				// 通配符映射暂不验证（需要具体请求才能展开）
				continue
			}

			// 精确映射需要验证
			supported := false
			if p.SupportedModels[internalModel] {
				supported = true
			} else {
				// 检查通配符白名单
				for supportedPattern := range p.SupportedModels {
					if matchWildcard(supportedPattern, internalModel) {
						supported = true
						break
					}
				}
			}

			if !supported {
				errors = append(errors, fmt.Sprintf(
					"模型映射无效：'%s' -> '%s'，目标模型 '%s' 不在 supportedModels 中",
					externalModel, internalModel, internalModel,
				))
			}
		}
	}

	for model, category := range p.ModelCategories {
		if strings.TrimSpace(model) == "" {
			errors = append(errors, "模型类别的模型名不能为空")
			continue
		}
		if normalizeModelCategory(category) == "" {
			errors = append(errors, fmt.Sprintf("模型类别无效：'%s' -> '%s'，允许值为 chat/speech/embedding/image/video", model, category))
		}
	}

	// 允许仅配置 modelMapping（无 supportedModels 时不阻塞保存）
	// 用户可能只想映射模型名，不需要白名单过滤

	// 规则 3 移除：自映射不会破坏功能，最多是无效配置，不阻塞保存

	p.configErrors = errors
	return errors
}

func normalizeModelCategory(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "chat", "speech", "embedding", "image", "video":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

// matchWildcard 通配符匹配函数
// 支持 * 通配符，如 "claude-*" 匹配 "claude-sonnet-4"
func matchWildcard(pattern, text string) bool {
	// 如果没有通配符，使用精确匹配
	if !strings.Contains(pattern, "*") {
		return pattern == text
	}

	// 简化实现：只支持单个 * 通配符
	parts := strings.Split(pattern, "*")
	if len(parts) == 2 {
		// 前缀 + * 或 * + 后缀
		prefix, suffix := parts[0], parts[1]
		return strings.HasPrefix(text, prefix) && strings.HasSuffix(text, suffix)
	}

	// 多个 * 的情况（更复杂，暂不支持）
	return false
}

// applyWildcardMapping 应用通配符映射
// 将 pattern 中的 * 匹配部分替换到 replacement 的 * 位置
// 示例: pattern="claude-*", replacement="anthropic/claude-*", input="claude-sonnet-4"
//
//	输出: "anthropic/claude-sonnet-4"
func applyWildcardMapping(pattern, replacement, input string) string {
	// 如果 pattern 或 replacement 没有通配符，直接返回 replacement
	if !strings.Contains(pattern, "*") || !strings.Contains(replacement, "*") {
		return replacement
	}

	// 提取通配符匹配的部分
	parts := strings.Split(pattern, "*")
	if len(parts) != 2 {
		return replacement // 不支持多个通配符
	}

	prefix, suffix := parts[0], parts[1]

	// 验证 input 确实匹配 pattern
	if !strings.HasPrefix(input, prefix) || !strings.HasSuffix(input, suffix) {
		return replacement
	}

	// 提取中间部分
	wildcardPart := input[len(prefix) : len(input)-len(suffix)]

	// 替换 replacement 中的 *
	return strings.Replace(replacement, "*", wildcardPart, 1)
}
