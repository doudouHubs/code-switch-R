package modelpricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

//go:embed model_prices_and_context_window.json
var pricingFile []byte

//go:embed model_prices_overlay.json
var overlayFile []byte

var (
	defaultOnce    sync.Once
	defaultService *Service
	defaultErr     error
	nameReplacer   = strings.NewReplacer("-", "", "_", "", ".", "", ":", "", "/", "", " ", "")
)

// familyRules 定义裸名 -> vendor 前缀的家族映射,顺序决定匹配优先级。
// 保留确定性,不使用 map 遍历(避免随机命中)。
var familyRules = []struct {
	Prefix      string
	Replacement string
}{
	{Prefix: "qwen3-", Replacement: "dashscope/qwen3-"},
	{Prefix: "qwen-", Replacement: "dashscope/qwen-"},
	{Prefix: "kimi-", Replacement: "moonshot/kimi-"},
	{Prefix: "moonshot-v1-", Replacement: "moonshot/moonshot-v1-"},
}

// Service 提供模型价格相关的计算能力。
type Service struct {
	pricingMap   map[string]*PricingEntry
	normalized   map[string]string
	ephemeral1h  map[string]float64
	longContexts map[string]LongContextPricing
}

// PricingEntry 映射 JSON 内的字段。
type PricingEntry struct {
	InputCostPerToken  float64 `json:"input_cost_per_token"`
	OutputCostPerToken float64 `json:"output_cost_per_token"`
	OutputCostPerImage float64 `json:"output_cost_per_image"`
	// LiteLLM 的图片模型把单张生成价按像素记录在 input_cost_per_pixel；
	// output_cost_per_pixel 通常为 0，但仍保留并优先读取，兼容两种字段口径。
	InputCostPerPixel           float64 `json:"input_cost_per_pixel"`
	OutputCostPerPixel          float64 `json:"output_cost_per_pixel"`
	OutputCostPerReasoningToken float64 `json:"output_cost_per_reasoning_token"`
	CacheCreationInputTokenCost float64 `json:"cache_creation_input_token_cost"`
	CacheReadInputTokenCost     float64 `json:"cache_read_input_token_cost"`
	// DeepSeek 等将 cache_read 以 cache_hit 命名,当 cache_read 缺失时作回退。
	InputCostPerTokenCacheHit float64 `json:"input_cost_per_token_cache_hit"`

	// 128k 档(少数 Gemini 系列)
	InputCostPerTokenAbove128k  float64 `json:"input_cost_per_token_above_128k_tokens"`
	OutputCostPerTokenAbove128k float64 `json:"output_cost_per_token_above_128k_tokens"`

	// 200k 档(Anthropic 长上下文 Sonnet)
	InputCostPerTokenAbove200k          float64 `json:"input_cost_per_token_above_200k_tokens"`
	OutputCostPerTokenAbove200k         float64 `json:"output_cost_per_token_above_200k_tokens"`
	CacheCreationInputTokenCostAbove200 float64 `json:"cache_creation_input_token_cost_above_200k_tokens"`
	CacheReadInputTokenCostAbove200k    float64 `json:"cache_read_input_token_cost_above_200k_tokens"`

	// 272k 档(GPT-5.x 系列)
	InputCostPerTokenAbove272k          float64 `json:"input_cost_per_token_above_272k_tokens"`
	OutputCostPerTokenAbove272k         float64 `json:"output_cost_per_token_above_272k_tokens"`
	CacheCreationInputTokenCostAbove272 float64 `json:"cache_creation_input_token_cost_above_272k_tokens"`
	CacheReadInputTokenCostAbove272k    float64 `json:"cache_read_input_token_cost_above_272k_tokens"`

	// Cache 1h(Anthropic ephemeral-1h)
	CacheCreationInputTokenCostAbove1Hr         float64 `json:"cache_creation_input_token_cost_above_1hr"`
	CacheCreationInputTokenCostAbove1HrAbove200 float64 `json:"cache_creation_input_token_cost_above_1hr_above_200k_tokens"`

	// Priority service tier(OpenAI/Azure 提供的更贵但响应更快的档位)。
	InputCostPerTokenPriority                float64 `json:"input_cost_per_token_priority"`
	OutputCostPerTokenPriority               float64 `json:"output_cost_per_token_priority"`
	CacheReadInputTokenCostPriority          float64 `json:"cache_read_input_token_cost_priority"`
	InputCostPerTokenAbove200kPriority       float64 `json:"input_cost_per_token_above_200k_tokens_priority"`
	OutputCostPerTokenAbove200kPriority      float64 `json:"output_cost_per_token_above_200k_tokens_priority"`
	CacheReadInputTokenCostAbove200kPriority float64 `json:"cache_read_input_token_cost_above_200k_tokens_priority"`
	InputCostPerTokenAbove272kPriority       float64 `json:"input_cost_per_token_above_272k_tokens_priority"`
	OutputCostPerTokenAbove272kPriority      float64 `json:"output_cost_per_token_above_272k_tokens_priority"`
	CacheReadInputTokenCostAbove272kPriority float64 `json:"cache_read_input_token_cost_above_272k_tokens_priority"`

	// Flex service tier 低价异步档位字段,常用于非实时任务,定价通常与批处理同价。
	// 仅有基础三项;JSON 中无长上下文 flex 变体,长窗口由 scaleLongRate 按比例外推。
	InputCostPerTokenFlex       float64 `json:"input_cost_per_token_flex"`
	OutputCostPerTokenFlex      float64 `json:"output_cost_per_token_flex"`
	CacheReadInputTokenCostFlex float64 `json:"cache_read_input_token_cost_flex"`

	TieredPricing []TieredPricingBand `json:"tiered_pricing,omitempty"`
}

// ServiceTier 描述 OpenAI/Azure 等上游请求时选的服务档位,影响单价。
type ServiceTier string

const (
	// ServiceTierDefault 标准档位(空值/不指定时的默认行为)。
	ServiceTierDefault ServiceTier = ""
	// ServiceTierObservedDefault 上游明确回传 "default" 字面量,与 ServiceTierDefault 区分观测态。
	ServiceTierObservedDefault ServiceTier = "default"
	// ServiceTierStandard 部分平台用 "standard" 表示默认档,计费与 default 一致。
	ServiceTierStandard ServiceTier = "standard"
	// ServiceTierPriority 优先档位,上游单价更高但延迟更低。
	ServiceTierPriority ServiceTier = "priority"
	// ServiceTierFlex 低价异步档位,通常与批处理同价。
	ServiceTierFlex ServiceTier = "flex"
)

// NormalizeObservedServiceTier 把上游原始 tier 字符串归一化成 ServiceTier。
// 已知值原样返回(保留 ObservedDefault vs Default 的区分,用于审计)。
// 空值返回 ServiceTierDefault 且不触发 onUnknown。
// 未知非空值触发 onUnknown 回调(一次性告警由调用方实现),并原样返回 lower 后的字符串。
func NormalizeObservedServiceTier(raw string, onUnknown func(string)) ServiceTier {
	tier := strings.ToLower(strings.TrimSpace(raw))
	switch tier {
	case "":
		return ServiceTierDefault
	case string(ServiceTierObservedDefault):
		return ServiceTierObservedDefault
	case string(ServiceTierStandard):
		return ServiceTierStandard
	case string(ServiceTierPriority):
		return ServiceTierPriority
	case string(ServiceTierFlex):
		return ServiceTierFlex
	default:
		if onUnknown != nil {
			onUnknown(tier)
		}
		return ServiceTier(tier)
	}
}

// normalizeServiceTier 把 tier 折叠为 pricing 能消费的三档(default/priority/flex)。
// standard 与 observed-default 均归到 default;未知值按 default 计费。
func normalizeServiceTier(tier ServiceTier) ServiceTier {
	normalized := NormalizeObservedServiceTier(string(tier), nil)
	switch normalized {
	case ServiceTierPriority, ServiceTierFlex:
		return normalized
	default:
		return ServiceTierDefault
	}
}

// scaleLongRate 用短窗口 tier/default 的价格比,按比例缩放一个已解析好的长窗口默认价。
// longDefault 必须是调用方已完成 fallback 的有效长窗口单价,例如
// firstNonZero(aboveBandRate, baseRate)。
// 任一输入 <=0 时保守回退到 longDefault:longDefault<=0 说明调用前提不成立,
// tierBase/defaultBase 缺失则避免把长窗口单价降到 0。
func scaleLongRate(longDefault, tierBase, defaultBase float64) float64 {
	if longDefault <= 0 || tierBase <= 0 || defaultBase <= 0 {
		return longDefault
	}
	return longDefault * (tierBase / defaultBase)
}

// TieredPricingBand 表示 tiered_pricing 中的单段。range 语义为 [lo, hi),
// 上界值本身归入下一档(实现见 pickTier)。
type TieredPricingBand struct {
	Range                   [2]float64 `json:"range"`
	InputCostPerToken       float64    `json:"input_cost_per_token"`
	OutputCostPerToken      float64    `json:"output_cost_per_token"`
	CacheReadInputTokenCost float64    `json:"cache_read_input_token_cost,omitempty"`
}

// overlayConfig 描述 overlay 文件的结构。
// Models 用于基础价表尚未发布的临时模型，Aliases 仅用于完全同价的模型别名。
type overlayConfig struct {
	Models  map[string]PricingEntry `json:"models"`
	Aliases map[string]string       `json:"aliases"`
}

// UsageSnapshot 描述一次请求的 token 用量。
type UsageSnapshot struct {
	InputTokens       int
	OutputTokens      int
	ReasoningTokens   int
	CacheCreateTokens int
	CacheReadTokens   int
	ImageCount        int
	ImageWidth        int
	ImageHeight       int
	CacheCreation     *CacheCreationDetail
	// ServiceTier 当前请求实际走的服务档位;空值视为 default,不影响定价。
	ServiceTier ServiceTier
}

// CacheCreationDetail 细分缓存创建 tokens。
type CacheCreationDetail struct {
	Ephemeral5mTokens int
	Ephemeral1hTokens int
}

// CostBreakdown 表示一次费用计算的结果。
type CostBreakdown struct {
	InputCost       float64 `json:"input_cost"`
	OutputCost      float64 `json:"output_cost"`
	ReasoningCost   float64 `json:"reasoning_cost"`
	ImageCost       float64 `json:"image_cost"`
	CacheCreateCost float64 `json:"cache_create_cost"`
	CacheReadCost   float64 `json:"cache_read_cost"`
	Ephemeral5mCost float64 `json:"ephemeral_5m_cost"`
	Ephemeral1hCost float64 `json:"ephemeral_1h_cost"`
	TotalCost       float64 `json:"total_cost"`
	HasPricing      bool    `json:"has_pricing"`
	IsLongContext   bool    `json:"is_long_context"`
	IsTiered        bool    `json:"is_tiered"`
}

// LongContextPricing 描述 1M 上下文模型的单价。
type LongContextPricing struct {
	Input  float64
	Output float64
}

// DefaultService 返回单例。
func DefaultService() (*Service, error) {
	defaultOnce.Do(func() {
		defaultService, defaultErr = NewService()
	})
	return defaultService, defaultErr
}

// NewService 从嵌入的 JSON 创建服务实例。
func NewService() (*Service, error) {
	raw := make(map[string]PricingEntry)
	if err := json.Unmarshal(pricingFile, &raw); err != nil {
		return nil, fmt.Errorf("parse pricing file: %w", err)
	}
	// litellm 首条 sample_spec 是 schema 文档,不是真实模型。
	delete(raw, "sample_spec")

	pricing := make(map[string]*PricingEntry, len(raw))
	normalized := make(map[string]string, len(raw))
	for key, entry := range raw {
		item := entry
		ensureCachePricing(&item)
		pricing[key] = &item
		norm := normalizeName(key)
		if _, exists := normalized[norm]; !exists {
			normalized[norm] = key
		}
	}

	// 合并 overlay:临时模型可携带独立价格；只有确认完全同价时才使用 alias 复用指针。
	var overlay overlayConfig
	if len(overlayFile) > 0 {
		if err := json.Unmarshal(overlayFile, &overlay); err != nil {
			return nil, fmt.Errorf("parse overlay file: %w", err)
		}
		for model, entry := range overlay.Models {
			if _, exists := pricing[model]; exists {
				return nil, fmt.Errorf("overlay model %q conflicts with base pricing", model)
			}
			if err := validateOverlayModelPricing(model, entry); err != nil {
				return nil, err
			}
			item := entry
			ensureCachePricing(&item)
			pricing[model] = &item
			norm := normalizeName(model)
			if _, exists := normalized[norm]; !exists {
				normalized[norm] = model
			}
		}
		for alias, target := range overlay.Aliases {
			entry, ok := pricing[target]
			if !ok {
				return nil, fmt.Errorf("overlay alias %q -> %q: target not found in base pricing", alias, target)
			}
			pricing[alias] = entry
			normAlias := normalizeName(alias)
			if _, exists := normalized[normAlias]; !exists {
				normalized[normAlias] = alias
			}
		}
	}

	return &Service{
		pricingMap:   pricing,
		normalized:   normalized,
		ephemeral1h:  buildEphemeral1hPricing(),
		longContexts: buildLongContextPricing(),
	}, nil
}

// validateOverlayModelPricing 在启动时拒绝不完整的临时模型价格。
// 费用统计宁可显式失败，也不能把高用量模型静默按 0 美元展示。
func validateOverlayModelPricing(model string, entry PricingEntry) error {
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("overlay model name is empty")
	}
	if entry.InputCostPerToken <= 0 || entry.OutputCostPerToken <= 0 {
		return fmt.Errorf("overlay model %q must define positive input and output prices", model)
	}
	return nil
}

// CalculateCost 根据模型与 token 用量返回费用明细(美元)。
func (s *Service) CalculateCost(model string, usage UsageSnapshot) CostBreakdown {
	if s == nil || model == "" {
		return CostBreakdown{}
	}
	entry, hasPricing := s.getPricing(model)
	breakdown := CostBreakdown{HasPricing: hasPricing}
	if entry == nil && !strings.Contains(strings.ToLower(model), "[1m]") {
		return breakdown
	}
	longTier, useLong := s.longContextTier(model, usage)
	if entry == nil {
		entry = &PricingEntry{}
	}

	totalPromptTokens := usage.InputTokens + usage.CacheCreateTokens + usage.CacheReadTokens
	tier := normalizeServiceTier(usage.ServiceTier)

	// 长上下文档位只解析一次,tiered 场景跳过(tiered 优先级更高)。
	var longBand longContextBand
	if len(entry.TieredPricing) == 0 {
		longBand = entry.resolveLongContextBand(totalPromptTokens, tier)
	}

	// 默认档价格,按 tier 重新取值(priority/flex 吃对应 *_priority/*_flex 字段)
	baseInput := entry.InputCostPerToken
	baseOutput := entry.OutputCostPerToken
	baseCacheRead := entry.CacheReadInputTokenCost
	switch tier {
	case ServiceTierPriority:
		baseInput = firstNonZero(entry.InputCostPerTokenPriority, baseInput)
		baseOutput = firstNonZero(entry.OutputCostPerTokenPriority, baseOutput)
		baseCacheRead = firstNonZero(entry.CacheReadInputTokenCostPriority, baseCacheRead)
	case ServiceTierFlex:
		baseInput = firstNonZero(entry.InputCostPerTokenFlex, baseInput)
		baseOutput = firstNonZero(entry.OutputCostPerTokenFlex, baseOutput)
		baseCacheRead = firstNonZero(entry.CacheReadInputTokenCostFlex, baseCacheRead)
	}

	// 价格档位选择优先级:tiered_pricing > longContextTier > above_272k > above_200k > above_128k > 基础价。
	switch {
	case len(entry.TieredPricing) > 0:
		band := pickTier(entry.TieredPricing, totalPromptTokens)
		breakdown.IsTiered = true
		breakdown.InputCost = float64(usage.InputTokens) * band.InputCostPerToken
		breakdown.OutputCost = float64(usage.OutputTokens) * band.OutputCostPerToken
		breakdown.CacheReadCost = float64(usage.CacheReadTokens) *
			firstNonZero(band.CacheReadInputTokenCost, baseCacheRead)
	case useLong:
		breakdown.IsLongContext = true
		breakdown.InputCost = float64(usage.InputTokens) * longTier.Input
		breakdown.OutputCost = float64(usage.OutputTokens) * longTier.Output
		breakdown.CacheReadCost = float64(usage.CacheReadTokens) * baseCacheRead
	case longBand.active:
		breakdown.IsLongContext = true
		breakdown.InputCost = float64(usage.InputTokens) * longBand.inputPerTok
		breakdown.OutputCost = float64(usage.OutputTokens) * longBand.outputPerTok
		breakdown.CacheReadCost = float64(usage.CacheReadTokens) * longBand.cacheRead
	default:
		breakdown.InputCost = float64(usage.InputTokens) * baseInput
		breakdown.OutputCost = float64(usage.OutputTokens) * baseOutput
		breakdown.CacheReadCost = float64(usage.CacheReadTokens) * baseCacheRead
	}

	if usage.ReasoningTokens > 0 && entry.OutputCostPerReasoningToken > 0 {
		breakdown.ReasoningCost = float64(usage.ReasoningTokens) * entry.OutputCostPerReasoningToken
	}

	cacheCreateTokens, cache1hTokens := resolveCacheTokens(usage)
	cache5mRate := entry.CacheCreationInputTokenCost
	// 1h 价取值优先级:longBand.cacheCreate1h > JSON above_1hr 字段 > 硬编码兜底
	cache1hRate := firstNonZero(entry.CacheCreationInputTokenCostAbove1Hr, s.getEphemeral1hPricing(model))
	if longBand.active {
		cache5mRate = firstNonZero(longBand.cacheCreate, entry.CacheCreationInputTokenCost)
		cache1hRate = firstNonZero(longBand.cacheCreate1h, cache1hRate)
	}
	cache5mCost := float64(cacheCreateTokens) * cache5mRate
	cache1hCost := float64(cache1hTokens) * cache1hRate
	breakdown.Ephemeral5mCost = cache5mCost
	breakdown.Ephemeral1hCost = cache1hCost
	breakdown.CacheCreateCost = cache5mCost + cache1hCost
	// 图片按 provider 实际返回的张数计费，不把 image_count 伪装成 token，避免首页和明细重复计算。
	imageCount := usage.ImageCount
	if imageCount < 0 {
		imageCount = 0
	}
	if imageCount > 0 {
		imageCostPerPixel := firstNonZero(entry.OutputCostPerPixel, entry.InputCostPerPixel)
		switch {
		case entry.OutputCostPerImage > 0:
			breakdown.ImageCost = float64(imageCount) * entry.OutputCostPerImage
		case imageCostPerPixel > 0:
			// 图片价格表的 pixel 费率代表一张输出图的生成价；没有尺寸的旧日志按
			// 当前服务默认 1024x1024 估算，避免历史成功请求继续被静默记为 0 元。
			pixels := resolveImagePixels(model, usage)
			breakdown.ImageCost = float64(imageCount) * float64(pixels) * imageCostPerPixel
		}
	}
	breakdown.TotalCost = breakdown.InputCost + breakdown.OutputCost + breakdown.ReasoningCost + breakdown.ImageCost + breakdown.CacheCreateCost + breakdown.CacheReadCost
	if breakdown.TotalCost > 0 {
		breakdown.HasPricing = true
	}
	return breakdown
}

const defaultImagePixelCount int64 = 1024 * 1024

// resolveImagePixels 优先使用请求日志保存的尺寸，其次从价格模型变体的模型名中读取尺寸，
// 最后回退到服务默认尺寸。最后一档只服务于历史日志和 auto 尺寸，保证兼容性但不伪造 token。
func resolveImagePixels(model string, usage UsageSnapshot) int64 {
	if usage.ImageWidth > 0 && usage.ImageHeight > 0 {
		return int64(usage.ImageWidth) * int64(usage.ImageHeight)
	}
	if pixels, ok := imagePixelsFromModel(model); ok {
		return pixels
	}
	return defaultImagePixelCount
}

// imagePixelsFromModel 识别 LiteLLM 图片价格键中的 "1024-x-1024" 尺寸片段。
// 只解析明确的数字尺寸，避免把任意模型名文本误当成计价输入。
func imagePixelsFromModel(model string) (int64, bool) {
	for _, segment := range strings.Split(strings.ToLower(strings.TrimSpace(model)), "/") {
		parts := strings.Split(segment, "-x-")
		if len(parts) != 2 {
			continue
		}
		width, widthErr := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
		height, heightErr := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
			continue
		}
		return width * height, true
	}
	return 0, false
}

// pickTier 根据 prompt tokens 总数选择分段价,range 语义为 [lo, hi),
// 上界值归入下一档;超过最大 band 时返回最后一段。
func pickTier(bands []TieredPricingBand, totalTokens int) *TieredPricingBand {
	for i := range bands {
		b := &bands[i]
		lo, hi := int(b.Range[0]), int(b.Range[1])
		if totalTokens >= lo && totalTokens < hi {
			return b
		}
	}
	return &bands[len(bands)-1]
}

// longContextBand 描述超阈值档位计费值,所有字段都已解析好,直接乘 tokens 即可。
type longContextBand struct {
	active        bool
	inputPerTok   float64
	outputPerTok  float64
	cacheRead     float64
	cacheCreate   float64
	cacheCreate1h float64
}

// resolveLongContextBand 按 prompt tokens 选择 >272k / >200k / >128k 档,未超阈值返回 active=false。
// tier=priority 时回退顺序:组合档 priority > 基础 priority > 组合档 default。
// 保证 priority 长上下文永远不会低于"priority 基础单价",避免某些模型缺组合字段导致低计费
// (例:部分 272k+ 模型有 output_cost_per_token_priority 但无 output_cost_per_token_above_272k_tokens_priority)。
// tier=flex 时:JSON 无 *_flex_above_200k/272k 字段,由 scaleLongRate 按短窗 flex/default 比例外推长窗单价。
func (e *PricingEntry) resolveLongContextBand(totalPromptTokens int, tier ServiceTier) longContextBand {
	if totalPromptTokens > 272000 && e.InputCostPerTokenAbove272k > 0 {
		input := e.InputCostPerTokenAbove272k
		output := firstNonZero(e.OutputCostPerTokenAbove272k, e.OutputCostPerToken)
		cacheRead := firstNonZero(e.CacheReadInputTokenCostAbove272k, e.CacheReadInputTokenCost)
		switch tier {
		case ServiceTierPriority:
			input = firstNonZero(e.InputCostPerTokenAbove272kPriority, e.InputCostPerTokenPriority, input)
			output = firstNonZero(e.OutputCostPerTokenAbove272kPriority, e.OutputCostPerTokenPriority, output)
			cacheRead = firstNonZero(e.CacheReadInputTokenCostAbove272kPriority, e.CacheReadInputTokenCostPriority, cacheRead)
		case ServiceTierFlex:
			input = scaleLongRate(input, e.InputCostPerTokenFlex, e.InputCostPerToken)
			output = scaleLongRate(output, e.OutputCostPerTokenFlex, e.OutputCostPerToken)
			cacheRead = scaleLongRate(cacheRead, e.CacheReadInputTokenCostFlex, e.CacheReadInputTokenCost)
		}
		return longContextBand{
			active:        true,
			inputPerTok:   input,
			outputPerTok:  output,
			cacheRead:     cacheRead,
			cacheCreate:   firstNonZero(e.CacheCreationInputTokenCostAbove272, e.CacheCreationInputTokenCost),
			cacheCreate1h: 0,
		}
	}
	if totalPromptTokens > 200000 && e.InputCostPerTokenAbove200k > 0 {
		input := e.InputCostPerTokenAbove200k
		output := firstNonZero(e.OutputCostPerTokenAbove200k, e.OutputCostPerToken)
		cacheRead := firstNonZero(e.CacheReadInputTokenCostAbove200k, e.CacheReadInputTokenCost)
		switch tier {
		case ServiceTierPriority:
			input = firstNonZero(e.InputCostPerTokenAbove200kPriority, e.InputCostPerTokenPriority, input)
			output = firstNonZero(e.OutputCostPerTokenAbove200kPriority, e.OutputCostPerTokenPriority, output)
			cacheRead = firstNonZero(e.CacheReadInputTokenCostAbove200kPriority, e.CacheReadInputTokenCostPriority, cacheRead)
		case ServiceTierFlex:
			input = scaleLongRate(input, e.InputCostPerTokenFlex, e.InputCostPerToken)
			output = scaleLongRate(output, e.OutputCostPerTokenFlex, e.OutputCostPerToken)
			cacheRead = scaleLongRate(cacheRead, e.CacheReadInputTokenCostFlex, e.CacheReadInputTokenCost)
		}
		return longContextBand{
			active:        true,
			inputPerTok:   input,
			outputPerTok:  output,
			cacheRead:     cacheRead,
			cacheCreate:   firstNonZero(e.CacheCreationInputTokenCostAbove200, e.CacheCreationInputTokenCost),
			cacheCreate1h: e.CacheCreationInputTokenCostAbove1HrAbove200,
		}
	}
	if totalPromptTokens > 128000 && e.InputCostPerTokenAbove128k > 0 {
		return longContextBand{
			active:       true,
			inputPerTok:  e.InputCostPerTokenAbove128k,
			outputPerTok: firstNonZero(e.OutputCostPerTokenAbove128k, e.OutputCostPerToken),
			cacheRead:    e.CacheReadInputTokenCost,
			cacheCreate:  e.CacheCreationInputTokenCost,
		}
	}
	return longContextBand{}
}

// firstNonZero 返回第一个非零值,用于 fallback 链。
func firstNonZero(values ...float64) float64 {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

// getPricing 按确定性顺序查找模型定价,不再使用无序 substring 模糊匹配。
// 顺序:exact → region-stripped → anthropic-stripped → 别名(gpt-5-codex→gpt-5)→ normalized → family fallback。
func (s *Service) getPricing(model string) (*PricingEntry, bool) {
	if model == "" {
		return nil, false
	}

	candidates := buildCandidates(model)

	// 1. 精确匹配
	for _, c := range candidates {
		if entry, ok := s.pricingMap[c]; ok {
			return entry, true
		}
	}

	// 2. normalized 匹配
	for _, c := range candidates {
		if key, ok := s.normalized[normalizeName(c)]; ok {
			return s.pricingMap[key], true
		}
	}

	// 3. family fallback:裸名 → vendor 前缀
	for _, c := range candidates {
		for _, familyKey := range familyFallbackCandidates(c) {
			if entry, ok := s.pricingMap[familyKey]; ok {
				return entry, true
			}
			if key, ok := s.normalized[normalizeName(familyKey)]; ok {
				return s.pricingMap[key], true
			}
		}
	}

	return nil, false
}

// buildCandidates 生成该模型名的所有等价候选(按优先级去重)。
func buildCandidates(model string) []string {
	seen := make(map[string]bool, 4)
	out := make([]string, 0, 4)
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	add(model)
	if model == "gpt-5-codex" {
		add("gpt-5")
	}
	stripped := stripRegionPrefix(model)
	add(stripped)
	add(strings.TrimPrefix(stripped, "anthropic."))
	return out
}

// familyFallbackCandidates 根据硬编码家族规则生成候选键,顺序由 familyRules 决定。
func familyFallbackCandidates(model string) []string {
	var out []string
	for _, rule := range familyRules {
		if strings.HasPrefix(model, rule.Prefix) {
			out = append(out, rule.Replacement+strings.TrimPrefix(model, rule.Prefix))
		}
	}
	return out
}

// longContextTier 根据 [1m] 后缀匹配用户自定义的 1M 长上下文价目。
// 严格精确匹配,不再无序 fallback 到任意 tier(避免未来加新模型被随机命中)。
// 优先级:JSON 的 above_200k/272k 字段 > 此机制,见 CalculateCost。
func (s *Service) longContextTier(model string, usage UsageSnapshot) (LongContextPricing, bool) {
	totalInput := usage.InputTokens + usage.CacheCreateTokens + usage.CacheReadTokens
	if strings.Contains(strings.ToLower(model), "[1m]") && totalInput > 200000 {
		if tier, ok := s.longContexts[model]; ok {
			return tier, true
		}
	}
	return LongContextPricing{}, false
}

func (s *Service) getEphemeral1hPricing(model string) float64 {
	if price, ok := s.ephemeral1h[model]; ok {
		return price
	}
	name := strings.ToLower(model)
	switch {
	case strings.Contains(name, "opus"):
		return 0.00003
	case strings.Contains(name, "sonnet"):
		return 0.000006
	case strings.Contains(name, "haiku"):
		return 0.0000016
	default:
		return 0
	}
}

func ensureCachePricing(entry *PricingEntry) {
	if entry == nil {
		return
	}
	if entry.CacheCreationInputTokenCost == 0 && entry.InputCostPerToken > 0 {
		entry.CacheCreationInputTokenCost = entry.InputCostPerToken * 1.25
	}
	if entry.CacheReadInputTokenCost == 0 {
		// DeepSeek/novita/zai 等用 cache_hit 命名缓存命中价,优先吃它再退回 10% 兜底
		entry.CacheReadInputTokenCost = firstNonZero(entry.InputCostPerTokenCacheHit, entry.InputCostPerToken*0.1)
	}
}

func stripRegionPrefix(name string) string {
	lower := strings.ToLower(name)
	for _, prefix := range []string{"us.", "eu.", "apac."} {
		if strings.HasPrefix(lower, prefix) {
			return name[len(prefix):]
		}
	}
	return name
}

func normalizeName(name string) string {
	return nameReplacer.Replace(strings.ToLower(name))
}

func resolveCacheTokens(usage UsageSnapshot) (fiveMin int, oneHour int) {
	if usage.CacheCreation == nil {
		return usage.CacheCreateTokens, 0
	}
	five := usage.CacheCreation.Ephemeral5mTokens
	one := usage.CacheCreation.Ephemeral1hTokens
	remaining := usage.CacheCreateTokens - five - one
	if remaining > 0 {
		five += remaining
	}
	if five < 0 {
		five = 0
	}
	if one < 0 {
		one = 0
	}
	return five, one
}

func buildEphemeral1hPricing() map[string]float64 {
	return map[string]float64{
		"claude-opus-4-5":            0.00001,
		"claude-opus-4-5-20251101":   0.00001,
		"claude-opus-4-5-20250929":   0.00001,
		"claude-opus-4-1":            0.00003,
		"claude-opus-4-1-20250805":   0.00003,
		"claude-opus-4":              0.00003,
		"claude-opus-4-20250514":     0.00003,
		"claude-3-opus":              0.00003,
		"claude-3-opus-latest":       0.00003,
		"claude-3-opus-20240229":     0.00003,
		"claude-3-5-sonnet":          0.000006,
		"claude-3-5-sonnet-latest":   0.000006,
		"claude-3-5-sonnet-20241022": 0.000006,
		"claude-3-5-sonnet-20240620": 0.000006,
		"claude-3-sonnet":            0.000006,
		"claude-3-sonnet-20240307":   0.000006,
		"claude-sonnet-3":            0.000006,
		"claude-sonnet-3-5":          0.000006,
		"claude-sonnet-3-7":          0.000006,
		"claude-sonnet-4":            0.000006,
		"claude-sonnet-4-20250514":   0.000006,
		"claude-3-5-haiku":           0.0000016,
		"claude-3-5-haiku-latest":    0.0000016,
		"claude-3-5-haiku-20241022":  0.0000016,
		"claude-3-haiku":             0.0000016,
		"claude-3-haiku-20240307":    0.0000016,
		"claude-haiku-3":             0.0000016,
		"claude-haiku-3-5":           0.0000016,
		"claude-haiku-4-5":           0.000002,
		"claude-haiku-4-5-20251001":  0.000002,
	}
}

func buildLongContextPricing() map[string]LongContextPricing {
	return map[string]LongContextPricing{
		"claude-sonnet-4-20250514[1m]": {
			Input:  0.000006,
			Output: 0.0000225,
		},
	}
}
