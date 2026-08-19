package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ProviderModel 是 provider /v1/models 返回给前端的最小安全投影。
// API key、完整响应 metadata 和 provider 配置都不应跨过 Wails 边界。
type ProviderModel struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	ModelCategory string `json:"modelCategory,omitempty"`
}

// FetchModels 从指定 provider 的真实模型目录读取模型，不依赖 supportedModels 白名单。
// supportedModels 只负责请求校验和可选过滤，空 map 表示不限制，而不是没有模型。
func (ps *ProviderService) FetchModels(kind string, providerID int64) ([]ProviderModel, error) {
	if ps == nil {
		return nil, fmt.Errorf("provider service is unavailable")
	}
	providers, err := ps.LoadProviders(kind)
	if err != nil {
		return nil, err
	}
	for _, provider := range providers {
		if provider.ID != providerID {
			continue
		}
		if !provider.Enabled {
			return nil, fmt.Errorf("provider %q is disabled", provider.Name)
		}
		return fetchProviderModels(provider, nil)
	}
	return nil, fmt.Errorf("provider %d was not found", providerID)
}

func fetchProviderModels(provider Provider, client *http.Client) ([]ProviderModel, error) {
	apiURL := strings.TrimSpace(provider.APIURL)
	if apiURL == "" {
		return nil, fmt.Errorf("provider %q has no API URL", provider.Name)
	}
	requestURL := modelsURL(apiURL)
	request, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build models request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	applyProviderModelsAuth(request, provider)

	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request provider models: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		// 不把上游响应正文带回 UI，避免第三方错误页泄露 token 或内部 URL。
		return nil, fmt.Errorf("provider models request returned HTTP %d", response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read provider models: %w", err)
	}
	return decodeProviderModels(body)
}

func applyProviderModelsAuth(request *http.Request, provider Provider) {
	apiKey := provider.APIKey
	switch authType := strings.ToLower(strings.TrimSpace(provider.ConnectivityAuthType)); authType {
	case "x-api-key":
		request.Header.Set("x-api-key", apiKey)
		request.Header.Set("anthropic-version", "2023-06-01")
	case "", "bearer":
		request.Header.Set("Authorization", "Bearer "+apiKey)
	default:
		headerName := strings.TrimSpace(provider.ConnectivityAuthType)
		if headerName == "" || strings.EqualFold(headerName, "custom") {
			headerName = "Authorization"
		}
		request.Header.Set(headerName, apiKey)
	}
}

func decodeProviderModels(body []byte) ([]ProviderModel, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return []ProviderModel{}, nil
	}

	var items []json.RawMessage
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, fmt.Errorf("decode provider models: %w", err)
		}
	} else {
		var envelope struct {
			Data   []json.RawMessage `json:"data"`
			Models []json.RawMessage `json:"models"`
		}
		if err := json.Unmarshal(trimmed, &envelope); err != nil {
			return nil, fmt.Errorf("decode provider models: %w", err)
		}
		items = envelope.Data
		if len(items) == 0 {
			items = envelope.Models
		}
	}

	models := make([]ProviderModel, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		model, ok := decodeProviderModel(item)
		if !ok || model.ID == "" {
			continue
		}
		if _, exists := seen[model.ID]; exists {
			continue
		}
		seen[model.ID] = struct{}{}
		models = append(models, model)
	}
	sort.SliceStable(models, func(i, j int) bool {
		return strings.ToLower(models[i].ID) < strings.ToLower(models[j].ID)
	})
	return models, nil
}

func decodeProviderModel(raw json.RawMessage) (ProviderModel, bool) {
	var model struct {
		ID                        string `json:"id"`
		Name                      string `json:"name"`
		DisplayName               string `json:"display_name"`
		Display                   string `json:"displayName"`
		Slug                      string `json:"slug"`
		ModelCategory             string `json:"modelCategory"`
		Category                  string `json:"category"`
		Type                      string `json:"type"`
		SupportsImageGeneration   *bool  `json:"supportsImageGeneration"`
		SupportsImageGenerationV2 *bool  `json:"supports_image_generation"`
	}
	if len(bytes.TrimSpace(raw)) > 0 && bytes.TrimSpace(raw)[0] == '"' {
		var id string
		if err := json.Unmarshal(raw, &id); err != nil {
			return ProviderModel{}, false
		}
		id = strings.TrimSpace(id)
		return ProviderModel{
			ID:            id,
			Name:          id,
			ModelCategory: inferProviderModelCategory(id),
		}, id != ""
	}
	if err := json.Unmarshal(raw, &model); err != nil {
		return ProviderModel{}, false
	}
	id := strings.TrimSpace(model.ID)
	if id == "" {
		id = strings.TrimSpace(model.Slug)
	}
	if id == "" {
		return ProviderModel{}, false
	}
	name := strings.TrimSpace(model.Name)
	if name == "" {
		name = strings.TrimSpace(model.DisplayName)
	}
	if name == "" {
		name = strings.TrimSpace(model.Display)
	}
	if name == "" {
		name = id
	}

	// provider 自己声明的类别优先；没有类别时再读取图片能力标志，最后才按
	// 模型名做有限兜底。这样不会因为一个普通模型名包含“image”字样就误判，
	// 同时兼容 OpenAI /v1/models 只返回 id 的常见响应。
	category := firstProviderModelCategory(model.ModelCategory, model.Category, model.Type)
	if category == "" && ((model.SupportsImageGeneration != nil && *model.SupportsImageGeneration) ||
		(model.SupportsImageGenerationV2 != nil && *model.SupportsImageGenerationV2)) {
		category = "image"
	}
	if category == "" {
		category = inferProviderModelCategory(id)
	}
	return ProviderModel{ID: id, Name: name, ModelCategory: category}, true
}

func firstProviderModelCategory(values ...string) string {
	for _, value := range values {
		if category := normalizeProviderModelCategory(value); category != "" {
			return category
		}
	}
	return ""
}

func normalizeProviderModelCategory(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("-", "_", " ", "_").Replace(normalized)
	switch normalized {
	case "chat", "text", "language", "llm", "completion", "completions":
		return "chat"
	case "speech", "audio", "tts", "text_to_speech", "texttospeech":
		return "speech"
	case "embedding", "embeddings":
		return "embedding"
	case "image", "images", "image_generation", "imagegeneration", "text_to_image", "text2image":
		return "image"
	case "video", "video_generation", "videogeneration", "text_to_video", "text2video":
		return "video"
	default:
		return ""
	}
}

func inferProviderModelCategory(modelID string) string {
	normalized := strings.ToLower(strings.TrimSpace(modelID))
	if normalized == "" {
		return ""
	}
	// 只维护明确的图片模型家族，不使用“包含 image”这种过宽规则，避免
	// 把普通聊天模型、embedding 模型或厂商自定义模型错误放进梦境图片列表。
	for _, marker := range []string{
		"gpt-image",
		"dall-e",
		"imagen",
		"flux",
		"stable-diffusion",
		"stable_diffusion",
		"sdxl",
		"seedream",
		"qwen-image",
		"qwen_image",
		"image-gen",
		"image_generation",
		"imagegen",
	} {
		if strings.Contains(normalized, marker) {
			return "image"
		}
	}
	return ""
}
