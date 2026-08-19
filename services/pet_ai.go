package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	modelpricing "codeswitch/resources/model-pricing"
)

// PetAIErrorCode 是宠物 AI 运行时错误的稳定错误码。
// 上游响应正文、provider 配置错误和事件 emitter 错误都不能直接作为公开文案，
// 调用方只能依赖错误码和有限的 HTTP 状态字段做分支。
type PetAIErrorCode string

const (
	PET_AI_INVALID_REQUEST        PetAIErrorCode = "PET_AI_INVALID_REQUEST"
	PET_AI_DEPENDENCY_UNAVAILABLE PetAIErrorCode = "PET_AI_DEPENDENCY_UNAVAILABLE"
	PET_AI_REQUEST_IN_FLIGHT      PetAIErrorCode = "PET_AI_REQUEST_IN_FLIGHT"
	PET_AI_EVENT_ERROR            PetAIErrorCode = "PET_AI_EVENT_ERROR"
	PET_AI_REQUEST_CANCELLED      PetAIErrorCode = "PET_AI_REQUEST_CANCELLED"
	PET_AI_TIMEOUT                PetAIErrorCode = "PET_AI_TIMEOUT"
	PET_AI_REQUEST_TOO_LARGE      PetAIErrorCode = "PET_AI_REQUEST_TOO_LARGE"
	PET_AI_RESPONSE_TOO_LARGE     PetAIErrorCode = "PET_AI_RESPONSE_TOO_LARGE"
	PET_AI_SSE_INVALID            PetAIErrorCode = "PET_AI_SSE_INVALID"
	PET_AI_RESPONSE_INVALID       PetAIErrorCode = "PET_AI_RESPONSE_INVALID"
	PET_AI_MEDIA_TYPE_INVALID     PetAIErrorCode = "PET_AI_MEDIA_TYPE_INVALID"
	PET_AI_UPSTREAM_ERROR         PetAIErrorCode = "PET_UPSTREAM_ERROR"
	PET_AI_AUDIO_QUEUE_FULL       PetAIErrorCode = "PET_AI_AUDIO_QUEUE_FULL"
	PET_AI_AUDIO_QUEUE_CLOSED     PetAIErrorCode = "PET_AI_AUDIO_QUEUE_CLOSED"
)

// 这些 provider 错误码属于已有 provider 引用错误族；常量放在本文件是为了让
// AI 执行层在发现“已解析引用无法变成可调用协议”时仍返回同一结构化错误。
const (
	PET_PROVIDER_CONFIG_INVALID   PetProviderErrorCode = "PET_PROVIDER_CONFIG_INVALID"
	PET_PROVIDER_PROTOCOL_INVALID PetProviderErrorCode = "PET_PROVIDER_PROTOCOL_INVALID"
)

const (
	PetAIDefaultTimeout                = 90 * time.Second
	PetAIDefaultMaxResponseBytes int64 = 4 << 20
	PetAIDefaultMaxAudioBytes    int64 = 16 << 20
	PetAIDefaultMaxRequestBytes  int64 = 256 << 10

	PetAIMaxPetIDLength         = 128
	PetAIMaxRequestIDLength     = 128
	PetAIMaxProviderIDLength    = 256
	PetAIMaxModelLength         = 256
	PetAIMaxPersonaLength       = 32 << 10
	PetAIMaxUserTextLength      = 16 << 10
	PetAIMaxHistoryItems        = 32
	PetAIMaxHistoryItemLength   = 16 << 10
	PetAIMaxTotalInputLength    = 64 << 10
	PetAIMaxProjectFolderLength = 4 << 10
	PetAIMaxVoiceLength         = 128
	PetAIMaxVoiceTagLength      = 128
	PetAIMaxInstructionLength   = 4 << 10
	PetAIMaxImageCount          = 4
	PetAIMaxImageBytes          = 128 << 10
	PetAIMaxImageTotalBytes     = 192 << 10

	petAIDefaultMaxSSELineBytes  = 128 << 10
	petAIDefaultMaxSSEEventBytes = 512 << 10

	// 工具调用是桌宠聊天的受控 continuation，不允许沿用普通 Agent 的无限语义。
	// 这两个值既是默认值，也是 PetAIOptions 可配置值的硬上限，防止一次请求
	// 通过恶意 provider 响应消耗过多磁盘读取和网络轮次。
	PetAIDefaultMaxToolContinuationRounds = 8
	PetAIDefaultMaxToolCalls              = 32
)

// PetAIError 不携带上游正文，也不把底层 error 文本暴露到 Error()。
// Status 只保留 HTTP 状态数字，便于 UI 做有限重试提示，不会把响应中的敏感内容带出去。
type PetAIError struct {
	Code   PetAIErrorCode `json:"code"`
	Status int            `json:"status,omitempty"`
	cause  error
}

func (e *PetAIError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Status > 0 {
		return string(e.Code) + " (HTTP " + strconv.Itoa(e.Status) + ")"
	}
	return string(e.Code)
}

func (e *PetAIError) Unwrap() error {
	if e == nil {
		return nil
	}
	// 只保留 context 的标准身份；上游/provider/emitter 的原始错误可能包含密钥，
	// 因此这些错误不进入 Unwrap 链，避免日志或 errors.As 绕过安全投影。
	if errors.Is(e.cause, context.Canceled) || errors.Is(e.cause, context.DeadlineExceeded) {
		return e.cause
	}
	return nil
}

func (e *PetAIError) Is(target error) bool {
	other, ok := target.(*PetAIError)
	return ok && e != nil && other != nil && e.Code == other.Code
}

// PetAIErrorCodeOf 返回错误链上的 AI 错误码；provider 引用错误仍按已有错误族返回。
func PetAIErrorCodeOf(err error) string {
	if code := PetProviderErrorCodeOf(err); code != "" {
		return string(code)
	}
	var petErr *PetAIError
	if errors.As(err, &petErr) && petErr != nil {
		return string(petErr.Code)
	}
	return ""
}

// PetAIEventType 是宠物 AI 流式事件的稳定类型。
type PetAIEventType string

const (
	PetAIEventStarted PetAIEventType = "started"
	PetAIEventDelta   PetAIEventType = "delta"
	// PetAIEventUsage 复用 PetStreamUsage 的稳定 wire value，主控可据此直接
	// 把 payload 转成 PetUsageEvent，再调用 PetService.AddExperienceFromUsage。
	PetAIEventUsage     PetAIEventType = PetAIEventType(PetStreamUsage)
	PetAIEventCompleted PetAIEventType = "completed"
	PetAIEventFailed    PetAIEventType = "failed"
	PetAIEventCancelled PetAIEventType = "cancelled"
)

// PetAIEventError 只允许把安全错误投影到事件中。
type PetAIEventError struct {
	Code      string `json:"code"`
	Status    int    `json:"status,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

// PetAIEvent 是 emitter 接收的统一 payload；它不包含 provider 配置或 API Key。
type PetAIEvent struct {
	Type      PetAIEventType         `json:"type"`
	PetID     string                 `json:"petId"`
	RequestID string                 `json:"requestId"`
	Sequence  int64                  `json:"sequence"`
	Delta     string                 `json:"delta,omitempty"`
	Text      string                 `json:"text,omitempty"`
	Usage     *PetStreamUsagePayload `json:"usage,omitempty"`
	Error     *PetAIEventError       `json:"error,omitempty"`
}

// PetAIEventEmitter 是事件输出的最小接口。事件发送失败会终止当前流，
// 但 emitter 返回的原始错误只留在服务内部，避免错误文本反射出 API Key。
type PetAIEventEmitter interface {
	Emit(event PetAIEvent) error
}

// PetAIEventEmitterFunc 让宿主可以直接注入闭包，而不需要额外定义适配器类型。
type PetAIEventEmitterFunc func(PetAIEvent) error

func (f PetAIEventEmitterFunc) Emit(event PetAIEvent) error {
	if f == nil {
		return nil
	}
	return f(event)
}

// PetAIProviderConfig 是 provider reader 返回给 AI 服务的受控运行时配置。
// APIKey、Headers 和 endpoint 只在请求执行期间使用，服务的任何公开结果都不会返回它们。
type PetAIProviderConfig struct {
	Resolution       PetProviderResolution `json:"resolution,omitempty"`
	Platform         string                `json:"platform,omitempty"`
	ProviderID       string                `json:"providerId,omitempty"`
	Model            string                `json:"model,omitempty"`
	EffectiveModel   string                `json:"effectiveModel,omitempty"`
	ModelCategory    string                `json:"modelCategory,omitempty"`
	BaseURL          string                `json:"-"`
	APIURL           string                `json:"-"`
	APIKey           string                `json:"-"`
	APIEndpoint      string                `json:"-"`
	SpeechEndpoint   string                `json:"-"`
	Protocol         string                `json:"protocol,omitempty"`
	UpstreamProtocol string                `json:"upstreamProtocol,omitempty"`
	AuthType         string                `json:"authType,omitempty"`
	AuthHeader       string                `json:"-"`
	Headers          map[string]string     `json:"-"`
	MaxOutputTokens  int                   `json:"maxOutputTokens,omitempty"`
	AudioMode        string                `json:"audioMode,omitempty"`
}

// PetAIProviderReader 是 provider 配置读取的窄接口。
// 它可以在宿主侧先用 PetProviderResolver 完成引用解析，再返回这个调用所需的临时配置；
// AI 服务不依赖 ProviderService/GeminiService 的具体 owner，也不持有可序列化的凭据结果。
type PetAIProviderReader interface {
	Read(ctx context.Context, reference PetProviderReference) (PetAIProviderConfig, error)
}

// PetWorkspaceResolver 只负责从后端事实源解析指定宠物的 workspace。
// 返回空字符串表示该宠物没有绑定项目；调用方不能把请求里的 projectFolder
// 当作解析失败时的回退值，否则前端就能借兼容字段越过 DAO 的宠物隔离边界。
type PetWorkspaceResolver interface {
	Resolve(ctx context.Context, petID string) (string, error)
}

// PetWorkspaceResolverFunc 让宿主可以把 DAO 适配成窄 resolver，而不把 PetDAO
// 的数据库职责耦合进 AI 请求服务。
type PetWorkspaceResolverFunc func(ctx context.Context, petID string) (string, error)

func (f PetWorkspaceResolverFunc) Resolve(ctx context.Context, petID string) (string, error) {
	if f == nil {
		return "", nil
	}
	return f(ctx, petID)
}

// PetAIHTTPTransport 只注入 RoundTrip，既能使用 http.DefaultTransport，也方便测试精确观察
// request.Context 的取消边界；服务不在内部偷偷创建不可取消的全局 client。
type PetAIHTTPTransport interface {
	RoundTrip(request *http.Request) (*http.Response, error)
}

// PetAIOptions 控制请求超时、请求/响应和 SSE 解析上限。
// 零值使用保守默认值，显式设置为负数会在构造时归一化为默认值。
type PetAIOptions struct {
	Timeout                   time.Duration
	MaxResponseBytes          int64
	MaxAudioBytes             int64
	MaxRequestBytes           int64
	MaxSSELineBytes           int
	MaxSSEEventBytes          int
	MaxToolContinuationRounds int
	MaxToolCalls              int
}

type PetAIDependencies struct {
	ProviderReader        PetAIProviderReader
	SpeechSelectionReader PetSpeechSelectionReader
	WorkspaceResolver     PetWorkspaceResolver
	Transport             PetAIHTTPTransport
	Emitter               PetAIEventEmitter
	AudioEmitter          PetAudioEventEmitter
	Options               PetAIOptions
}

// PetAIService 是不触碰宠物状态、窗口、atlas 和前端的独立 AI 请求边界。
type PetAIService struct {
	providerReader        PetAIProviderReader
	speechSelectionReader PetSpeechSelectionReader
	workspaceResolver     PetWorkspaceResolver
	transport             PetAIHTTPTransport
	emitter               PetAIEventEmitter
	audioEmitter          PetAudioEventEmitter
	options               PetAIOptions

	mu     sync.Mutex
	active map[string]*petAIRequestState
}

// NewPetAIService 使用默认边界构造 AI 服务。
func NewPetAIService(providerReader PetAIProviderReader, transport PetAIHTTPTransport, emitter PetAIEventEmitter) *PetAIService {
	return NewPetAIServiceWithDependencies(PetAIDependencies{
		ProviderReader: providerReader,
		Transport:      transport,
		Emitter:        emitter,
	})
}

// NewPetAIServiceWithOptions 是带边界配置的便捷构造器，便于宿主按环境收紧上限。
func NewPetAIServiceWithOptions(
	providerReader PetAIProviderReader,
	transport PetAIHTTPTransport,
	emitter PetAIEventEmitter,
	options PetAIOptions,
) *PetAIService {
	return NewPetAIServiceWithDependencies(PetAIDependencies{
		ProviderReader: providerReader,
		Transport:      transport,
		Emitter:        emitter,
		Options:        options,
	})
}

func NewPetAIServiceWithDependencies(deps PetAIDependencies) *PetAIService {
	return &PetAIService{
		providerReader:        deps.ProviderReader,
		speechSelectionReader: deps.SpeechSelectionReader,
		workspaceResolver:     deps.WorkspaceResolver,
		transport:             deps.Transport,
		emitter:               deps.Emitter,
		audioEmitter:          deps.AudioEmitter,
		options:               normalizePetAIOptions(deps.Options),
		active:                make(map[string]*petAIRequestState),
	}
}

// PetAIImage 是视觉输入的受控形状。Data 只保存无 data-url 前缀的标准 base64，
// 这样 provider 适配层可以按协议分别拼装，且不会把任意 URL 或本地路径透传给上游。
type PetAIImage struct {
	Data      string `json:"data"`
	MediaType string `json:"mediaType"`
}

// PetAIMessage 是宠物聊天历史的最小形状；图片只允许作为当前已支持的视觉附件。
type PetAIMessage struct {
	Role    string       `json:"role"`
	Content string       `json:"content"`
	Images  []PetAIImage `json:"images,omitempty"`
}

type PetChatRequest struct {
	PetID         string               `json:"petId"`
	RequestID     string               `json:"requestId"`
	Provider      PetProviderReference `json:"provider"`
	Persona       string               `json:"persona"`
	UserText      string               `json:"userText"`
	Images        []PetAIImage         `json:"images,omitempty"`
	History       []PetAIMessage       `json:"history,omitempty"`
	ProjectFolder string               `json:"projectFolder,omitempty"`
	Reasoning     string               `json:"reasoning,omitempty"`
}

// PetDreamTextRequest 与聊天共享完全相同的输入边界，确保梦境不会绕过长度、provider
// 引用和 requestId 并发校验；执行时只是不发流式事件。
type PetDreamTextRequest struct {
	PetID     string               `json:"petId"`
	RequestID string               `json:"requestId"`
	Provider  PetProviderReference `json:"provider"`
	Persona   string               `json:"persona"`
	UserText  string               `json:"userText"`
	History   []PetAIMessage       `json:"history,omitempty"`
	Reasoning string               `json:"reasoning,omitempty"`
}

type PetChatStartResult struct {
	RequestID string `json:"requestId"`
}

// PetSpeechRequest 同时服务同步和流式语音；流式入口要求 RequestID 非空，
// 这样取消与音频事件都能绑定到同一条请求，不会把旧句子的 chunk 发给新请求。
type PetSpeechRequest struct {
	PetID       string               `json:"petId"`
	RequestID   string               `json:"requestId,omitempty"`
	Provider    PetProviderReference `json:"provider"`
	Text        string               `json:"text"`
	Voice       string               `json:"voice,omitempty"`
	Instruction string               `json:"instruction,omitempty"`
	VoiceMode   PetVoiceMode         `json:"voiceMode,omitempty"`
	VoiceTag    string               `json:"voiceTag,omitempty"`
}

type PetTTSRequest = PetSpeechRequest

type petAIRequestState struct {
	cancel   context.CancelFunc
	sequence int64
}

type petAIChatInput struct {
	PetID         string
	RequestID     string
	Provider      PetProviderReference
	Persona       string
	UserText      string
	Images        []PetAIImage
	History       []PetAIMessage
	ProjectFolder string
	Reasoning     string
}

type petAIProviderRuntime struct {
	reference       PetProviderReference
	platform        string
	providerID      string
	model           string
	modelCategory   string
	baseURL         string
	apiKey          string
	apiEndpoint     string
	speechEndpoint  string
	protocol        string
	authType        string
	authHeader      string
	headers         map[string]string
	maxOutputTokens int
	audioMode       string
}

func normalizePetAIOptions(options PetAIOptions) PetAIOptions {
	if options.Timeout <= 0 {
		options.Timeout = PetAIDefaultTimeout
	}
	if options.MaxResponseBytes <= 0 {
		options.MaxResponseBytes = PetAIDefaultMaxResponseBytes
	}
	if options.MaxAudioBytes <= 0 {
		options.MaxAudioBytes = PetAIDefaultMaxAudioBytes
	}
	if options.MaxRequestBytes <= 0 {
		options.MaxRequestBytes = PetAIDefaultMaxRequestBytes
	}
	if options.MaxSSELineBytes <= 0 {
		options.MaxSSELineBytes = petAIDefaultMaxSSELineBytes
	}
	if options.MaxSSEEventBytes <= 0 {
		options.MaxSSEEventBytes = petAIDefaultMaxSSEEventBytes
	}
	if options.MaxToolContinuationRounds <= 0 || options.MaxToolContinuationRounds > PetAIDefaultMaxToolContinuationRounds {
		options.MaxToolContinuationRounds = PetAIDefaultMaxToolContinuationRounds
	}
	if options.MaxToolCalls <= 0 || options.MaxToolCalls > PetAIDefaultMaxToolCalls {
		options.MaxToolCalls = PetAIDefaultMaxToolCalls
	}
	return options
}

// StartChat 先完成引用解析和 started 事件，再异步发起 HTTP 请求。
// 这样调用方拿到 requestId 时，事件序列已经有确定的起点，取消也能只针对这一条请求。
func (s *PetAIService) StartChat(ctx context.Context, request PetChatRequest) (PetChatStartResult, error) {
	input, err := s.normalizeChatRequest(request, PetCapabilityChat)
	if err != nil {
		return PetChatStartResult{}, err
	}
	if ctx == nil {
		return PetChatStartResult{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if s == nil || s.providerReader == nil || s.transport == nil || s.emitter == nil {
		return PetChatStartResult{}, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	// 只有普通聊天允许启用只读工具。workspace 必须由后端按 petId 解析，
	// 前端传入的兼容字段即使合法，也不能成为工具根目录或 resolver 失败时的回退。
	workspace, err := s.resolveChatWorkspace(ctx, input.PetID)
	if err != nil {
		return PetChatStartResult{}, err
	}
	input.ProjectFolder = workspace

	provider, err := s.resolveProvider(ctx, input.Provider, PetCapabilityChat)
	if err != nil {
		return PetChatStartResult{}, err
	}
	if ctx.Err() != nil {
		return PetChatStartResult{}, classifyPetAIContextError(ctx.Err())
	}

	requestCtx, cancel := context.WithTimeout(ctx, s.options.Timeout)
	state := &petAIRequestState{cancel: cancel}
	if err := s.reserveRequest(input.RequestID, state); err != nil {
		cancel()
		return PetChatStartResult{}, err
	}

	if err := s.emit(state, PetAIEvent{
		Type:      PetAIEventStarted,
		PetID:     input.PetID,
		RequestID: input.RequestID,
	}); err != nil {
		s.releaseRequest(input.RequestID, state)
		cancel()
		return PetChatStartResult{}, err
	}

	go s.runChat(requestCtx, state, input, provider)
	return PetChatStartResult{RequestID: input.RequestID}, nil
}

// CancelChat 对未知或已经结束的 requestId 返回 nil，保证重复取消不会制造新的错误状态。
// 对活动请求直接取消同一个 context，因此 transport 可以在 RoundTrip 内观察 Done。
func (s *PetAIService) CancelChat(requestID string) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || runeLen(requestID) > PetAIMaxRequestIDLength {
		return newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if s == nil {
		return nil
	}

	s.mu.Lock()
	state := s.active[requestID]
	s.mu.Unlock()
	if state == nil || state.cancel == nil {
		return nil
	}
	state.cancel()
	return nil
}

// GenerateDreamText 是同步文本入口，复用 executeText 的请求构造、SSE 解析和上限控制。
// 它不发 started/delta/completed 事件，避免后台梦境污染主动聊天流。
func (s *PetAIService) GenerateDreamText(ctx context.Context, request PetDreamTextRequest) (string, error) {
	input, err := s.normalizeChatRequest(PetChatRequest{
		PetID:     request.PetID,
		RequestID: request.RequestID,
		Provider:  request.Provider,
		Persona:   request.Persona,
		UserText:  request.UserText,
		History:   request.History,
		Reasoning: request.Reasoning,
	}, PetCapabilityChat)
	if err != nil {
		return "", err
	}
	if ctx == nil {
		return "", newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if s == nil || s.providerReader == nil || s.transport == nil {
		return "", newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}

	provider, err := s.resolveProvider(ctx, input.Provider, PetCapabilityChat)
	if err != nil {
		return "", err
	}
	requestCtx, cancel := context.WithTimeout(ctx, s.options.Timeout)
	defer cancel()

	state := &petAIRequestState{cancel: cancel}
	if err := s.reserveRequest(input.RequestID, state); err != nil {
		return "", err
	}
	defer s.releaseRequest(input.RequestID, state)

	text, err := s.executeText(requestCtx, provider, input, nil, nil)
	if err != nil {
		return "", classifyPetAIExecutionError(err, requestCtx)
	}
	return text, nil
}

// SynthesizeSpeech 返回一整段音频。speech 使用旧的 /audio/speech 协议，
// chat 使用非流式 chat/completions；本方法不产生 audio_delta 流式事件。
func (s *PetAIService) SynthesizeSpeech(ctx context.Context, request PetSpeechRequest) ([]byte, string, error) {
	input, err := s.normalizeSpeechRequest(request)
	if err != nil {
		return nil, "", err
	}
	if ctx == nil {
		return nil, "", newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if s == nil || s.providerReader == nil || s.transport == nil {
		return nil, "", newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}

	requestCtx, cancel := context.WithTimeout(ctx, s.options.Timeout)
	defer cancel()

	var state *petAIRequestState
	if input.RequestID != "" {
		state = &petAIRequestState{cancel: cancel}
		if err := s.reserveRequest(input.RequestID, state); err != nil {
			return nil, "", err
		}
		defer s.releaseRequest(input.RequestID, state)
	}
	// 同步 TTS 也允许通过 requestId 取消；活动登记必须覆盖 provider 解析阶段，
	// 否则 CancelSpeech 可能早于 provider 解析完成而失去唯一取消 owner。
	provider, err := s.resolveProvider(requestCtx, input.Provider, PetCapabilityTTS)
	if err != nil {
		return nil, "", err
	}

	mode, err := resolvePetSpeechMode(input.VoiceMode, provider)
	if err != nil {
		return nil, "", err
	}

	var audio []byte
	var mediaType string
	switch mode {
	case PetVoiceSpeech:
		// speech 保持原有协议和响应校验，避免 chat 音频被当作 PCM 或二进制直出。
		audio, mediaType, err = s.executeSpeech(requestCtx, provider, input)
	case PetVoiceChat:
		// chat-audio 走独立的完整 JSON 响应路径，不能回退到 /audio/speech。
		audio, mediaType, err = s.executeChatAudio(requestCtx, provider, input)
	default:
		return nil, "", newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if err != nil {
		return nil, "", classifyPetAIExecutionError(err, requestCtx)
	}
	return audio, mediaType, nil
}

func (s *PetAIService) runChat(
	ctx context.Context,
	state *petAIRequestState,
	input petAIChatInput,
	provider petAIProviderRuntime,
) {
	defer s.releaseRequest(input.RequestID, state)
	var usage modelpricing.UsageSnapshot
	usageSeen := false
	onUsage := func(next modelpricing.UsageSnapshot) {
		usage = mergePetAIUsage(usage, next)
		// input/output 是宠物经验的最小计费事实；只有这两个字段至少一个为正时，
		// 才发送 usage 事件，避免 provider 仅回传空 usage 对聊天主控造成假入账。
		usageSeen = usage.InputTokens > 0 || usage.OutputTokens > 0
	}

	text, err := s.executeText(ctx, provider, input, func(delta string) error {
		return s.emit(state, PetAIEvent{
			Type:      PetAIEventDelta,
			PetID:     input.PetID,
			RequestID: input.RequestID,
			Delta:     delta,
		})
	}, onUsage)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) && !isPetAIEventError(err) {
			_ = s.emit(state, PetAIEvent{
				Type:      PetAIEventCancelled,
				PetID:     input.PetID,
				RequestID: input.RequestID,
			})
			return
		}
		_ = s.emit(state, PetAIEvent{
			Type:      PetAIEventFailed,
			PetID:     input.PetID,
			RequestID: input.RequestID,
			Error:     publicPetAIEventError(err, ctx),
		})
		return
	}

	if ctx.Err() != nil {
		_ = s.emit(state, PetAIEvent{
			Type:      PetAIEventCancelled,
			PetID:     input.PetID,
			RequestID: input.RequestID,
		})
		return
	}
	if usageSeen {
		// usage 事件不是聊天成功的前置条件；emitter 或下游经验接线失败时，
		// 仍必须继续发送 completed，保证 usage 账务不会反向打断用户聊天。
		_ = s.emit(state, PetAIEvent{
			Type:      PetAIEventUsage,
			PetID:     input.PetID,
			RequestID: input.RequestID,
			Usage:     petAIUsagePayload(input.RequestID, provider, usage),
		})
	}
	_ = s.emit(state, PetAIEvent{
		Type:      PetAIEventCompleted,
		PetID:     input.PetID,
		RequestID: input.RequestID,
		Text:      text,
	})
}

func (s *PetAIService) normalizeChatRequest(request PetChatRequest, capability PetCapability) (petAIChatInput, error) {
	petID := strings.TrimSpace(request.PetID)
	requestID := strings.TrimSpace(request.RequestID)
	if petID == "" || runeLen(petID) > PetAIMaxPetIDLength || hasLineBreak(petID) {
		return petAIChatInput{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if requestID == "" || runeLen(requestID) > PetAIMaxRequestIDLength || hasLineBreak(requestID) {
		return petAIChatInput{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}

	providerRef, err := normalizePetAIReference(request.Provider, capability)
	if err != nil {
		return petAIChatInput{}, err
	}
	persona := strings.TrimSpace(request.Persona)
	userText := strings.TrimSpace(request.UserText)
	projectFolder, err := normalizePetAIProjectFolder(request.ProjectFolder)
	if err != nil {
		return petAIChatInput{}, err
	}
	images, _, err := normalizePetAIImages(request.Images)
	if err != nil {
		return petAIChatInput{}, err
	}
	if runeLen(persona) > PetAIMaxPersonaLength || (userText == "" && len(images) == 0) || runeLen(userText) > PetAIMaxUserTextLength {
		return petAIChatInput{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}

	history, totalHistoryLength, err := normalizePetAIHistory(request.History)
	if err != nil {
		return petAIChatInput{}, err
	}
	reasoning, err := normalizePetAIReasoning(request.Reasoning)
	if err != nil {
		return petAIChatInput{}, err
	}
	if runeLen(persona)+runeLen(userText)+runeLen(projectFolder)+totalHistoryLength > PetAIMaxTotalInputLength {
		return petAIChatInput{}, newPetAIError(PET_AI_REQUEST_TOO_LARGE, 0, nil)
	}

	return petAIChatInput{
		PetID:         petID,
		RequestID:     requestID,
		Provider:      providerRef,
		Persona:       persona,
		UserText:      userText,
		Images:        images,
		History:       history,
		ProjectFolder: projectFolder,
		Reasoning:     reasoning,
	}, nil
}

func normalizePetAIProjectFolder(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	// 项目目录最终会进入本地文件系统 API；换行和 NUL 不能依赖 JSON 转义兜底，
	// 相对路径也不能让服务进程工作目录成为隐式 workspace。
	if runeLen(value) > PetAIMaxProjectFolderLength || hasLineBreak(value) || strings.IndexByte(value, 0) >= 0 || !filepath.IsAbs(value) {
		return "", newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	return filepath.Clean(value), nil
}

func (s *PetAIService) resolveChatWorkspace(ctx context.Context, petID string) (string, error) {
	if s == nil || s.workspaceResolver == nil {
		return "", nil
	}
	workspace, err := s.workspaceResolver.Resolve(ctx, petID)
	if err != nil {
		// resolver 可能来自数据库或文件系统；原始错误不能穿过 Wails，
		// 但失败必须阻止请求偷偷退回到前端提供的路径。
		return "", newPetAIError(PET_AI_INVALID_REQUEST, 0, err)
	}
	workspace, err = normalizePetAIProjectFolder(workspace)
	if err != nil {
		return "", newPetAIError(PET_AI_INVALID_REQUEST, 0, err)
	}
	if workspace == "" {
		return "", nil
	}
	// 在 started 事件前确认目录存在且 symlink 根已被解析，避免把一个失效
	// 的持久化引用当成普通聊天继续执行；executeTextWithTools 仍会再次校验，
	// 用来覆盖请求启动后目录被删除或替换的竞态。
	executor, err := NewPetAgentToolExecutor(workspace)
	if err != nil {
		return "", newPetAIError(PET_AI_INVALID_REQUEST, 0, err)
	}
	return executor.WorkspaceRoot(), nil
}

func (s *PetAIService) normalizeSpeechRequest(request PetSpeechRequest) (PetSpeechRequest, error) {
	rawVoiceTag := request.VoiceTag
	request.PetID = strings.TrimSpace(request.PetID)
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.Text = strings.TrimSpace(request.Text)
	request.Voice = strings.TrimSpace(request.Voice)
	request.Instruction = strings.TrimSpace(request.Instruction)
	request.VoiceMode = PetVoiceMode(strings.ToLower(strings.TrimSpace(string(request.VoiceMode))))
	request.VoiceTag = strings.TrimSpace(request.VoiceTag)
	if request.VoiceMode == "" {
		request.VoiceMode = PetVoiceAuto
	}
	if request.PetID == "" || runeLen(request.PetID) > PetAIMaxPetIDLength || hasLineBreak(request.PetID) {
		return PetSpeechRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if request.RequestID != "" && (runeLen(request.RequestID) > PetAIMaxRequestIDLength || hasLineBreak(request.RequestID)) {
		return PetSpeechRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if request.Text == "" || runeLen(request.Text) > PetAIMaxUserTextLength {
		return PetSpeechRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if runeLen(request.Voice) > PetAIMaxVoiceLength || runeLen(request.Instruction) > PetAIMaxInstructionLength {
		return PetSpeechRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if request.VoiceMode != PetVoiceAuto && request.VoiceMode != PetVoiceSpeech && request.VoiceMode != PetVoiceChat {
		return PetSpeechRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	// tag 最终会进入上游 JSON message；换行会改变消息结构语义，因此不能只依赖 JSON 转义兜底。
	if runeLen(request.VoiceTag) > PetAIMaxVoiceTagLength || hasLineBreak(rawVoiceTag) {
		return PetSpeechRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	provider, err := normalizePetAIReference(request.Provider, PetCapabilityTTS)
	if err != nil {
		return PetSpeechRequest{}, err
	}
	request.Provider = provider
	return request, nil
}

func normalizePetAIReference(reference PetProviderReference, capability PetCapability) (PetProviderReference, error) {
	reference.Platform = strings.ToLower(strings.TrimSpace(reference.Platform))
	reference.ProviderID = strings.TrimSpace(reference.ProviderID)
	reference.Model = strings.TrimSpace(reference.Model)
	reference.Capability = PetCapability(strings.ToLower(strings.TrimSpace(string(reference.Capability))))
	if reference.Capability == "" {
		// 操作入口已经明确能力，空 capability 只补成当前入口的能力，不允许在不同能力间猜测。
		reference.Capability = capability
	}
	if reference.Platform == "" || reference.ProviderID == "" || reference.Model == "" {
		return reference, newPetProviderError(PET_REFERENCE_INVALID, reference, "provider 引用不完整", nil)
	}
	if runeLen(reference.ProviderID) > PetAIMaxProviderIDLength || runeLen(reference.Model) > PetAIMaxModelLength {
		return reference, newPetProviderError(PET_REFERENCE_INVALID, reference, "provider 引用超出长度限制", nil)
	}
	if reference.Capability != capability {
		return reference, newPetProviderError(PET_CAPABILITY_UNSUPPORTED, reference, "provider 引用能力不匹配", nil)
	}
	return reference, nil
}

func normalizePetAIHistory(history []PetAIMessage) ([]PetAIMessage, int, error) {
	if len(history) > PetAIMaxHistoryItems {
		return nil, 0, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if len(history) == 0 {
		return nil, 0, nil
	}

	normalized := make([]PetAIMessage, len(history))
	total := 0
	for index, item := range history {
		role := strings.ToLower(strings.TrimSpace(item.Role))
		content := strings.TrimSpace(item.Content)
		images, _, err := normalizePetAIImages(item.Images)
		if err != nil {
			return nil, 0, err
		}
		// 文本契约只接受 user/assistant，避免把未知 role 透传到三种协议后产生不同语义；
		// 图片-only 历史允许 content 为空，但不能留下完全没有内容的消息。
		if (role != "user" && role != "assistant") || (content == "" && len(images) == 0) || runeLen(content) > PetAIMaxHistoryItemLength {
			return nil, 0, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
		}
		normalized[index] = PetAIMessage{Role: role, Content: content, Images: images}
		total += runeLen(content)
	}
	return normalized, total, nil
}

func normalizePetAIImages(images []PetAIImage) ([]PetAIImage, int, error) {
	if len(images) == 0 {
		return nil, 0, nil
	}
	if len(images) > PetAIMaxImageCount {
		return nil, 0, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}

	normalized := make([]PetAIImage, len(images))
	totalBytes := 0
	for index, image := range images {
		mediaType := strings.ToLower(strings.TrimSpace(image.MediaType))
		if !isPetAIImageMediaType(mediaType) {
			return nil, 0, newPetAIError(PET_AI_MEDIA_TYPE_INVALID, 0, nil)
		}
		data := strings.TrimSpace(image.Data)
		// 只接受裸 base64；data URL、远程 URL 和本地路径都必须在入口处拒绝。
		if data == "" || strings.Contains(data, ",") || strings.ContainsAny(data, " \t\r\n") {
			return nil, 0, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
		}
		decoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil || len(decoded) == 0 {
			return nil, 0, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
		}
		if len(decoded) > PetAIMaxImageBytes || totalBytes > PetAIMaxImageTotalBytes-len(decoded) {
			return nil, 0, newPetAIError(PET_AI_REQUEST_TOO_LARGE, 0, nil)
		}
		totalBytes += len(decoded)
		normalized[index] = PetAIImage{
			Data:      base64.StdEncoding.EncodeToString(decoded),
			MediaType: mediaType,
		}
	}
	return normalized, totalBytes, nil
}

func isPetAIImageMediaType(mediaType string) bool {
	switch mediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func normalizePetAIReasoning(reasoning string) (string, error) {
	reasoning = strings.ToLower(strings.TrimSpace(reasoning))
	switch reasoning {
	case "", "none", "minimal", "low", "medium", "high":
		return reasoning, nil
	default:
		return "", newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
}

func (s *PetAIService) resolveProvider(
	ctx context.Context,
	reference PetProviderReference,
	capability PetCapability,
) (petAIProviderRuntime, error) {
	if s.providerReader == nil {
		return petAIProviderRuntime{}, newPetProviderError(PET_UPSTREAM_ERROR, reference, "provider 配置读取不可用", nil)
	}
	// provider 配置读取也属于请求前置链路，不能因为 reader 没有自行设置超时就无限阻塞；
	// 否则 StartChat 既无法返回 requestId，也无法让调用方通过 CancelChat 收敛这次启动。
	resolveCtx, cancel := context.WithTimeout(ctx, s.options.Timeout)
	defer cancel()
	config, err := s.providerReader.Read(resolveCtx, reference)
	if err != nil {
		if resolveCtx.Err() != nil {
			return petAIProviderRuntime{}, classifyPetAIContextError(resolveCtx.Err())
		}
		// 既有 PetProviderError 的 Code 可以安全保留；其它底层错误统一压成固定 upstream 码。
		if code := PetProviderErrorCodeOf(err); code != "" {
			return petAIProviderRuntime{}, newPetProviderError(code, reference, "provider 引用解析失败", nil)
		}
		return petAIProviderRuntime{}, newPetProviderError(PET_UPSTREAM_ERROR, reference, "读取 provider 配置失败", nil)
	}
	if resolveCtx.Err() != nil {
		return petAIProviderRuntime{}, classifyPetAIContextError(resolveCtx.Err())
	}

	runtime, err := normalizePetAIProviderConfig(config, reference, capability)
	if err != nil {
		return petAIProviderRuntime{}, err
	}
	return runtime, nil
}

func normalizePetAIProviderConfig(
	config PetAIProviderConfig,
	reference PetProviderReference,
	capability PetCapability,
) (petAIProviderRuntime, error) {
	platform := strings.ToLower(strings.TrimSpace(config.Platform))
	if platform == "" {
		platform = strings.ToLower(strings.TrimSpace(config.Resolution.Platform))
	}
	if platform == "" {
		platform = reference.Platform
	}
	providerID := strings.TrimSpace(config.ProviderID)
	if providerID == "" {
		providerID = strings.TrimSpace(config.Resolution.ProviderID)
	}
	if providerID == "" {
		providerID = reference.ProviderID
	}
	// usage 必须按实际上游调用的模型计价；EffectiveModel 是 provider resolver
	// 完成别名/路由映射后的 canonical 模型，不能被请求侧的 Model 覆盖。
	model := strings.TrimSpace(config.EffectiveModel)
	if model == "" {
		model = strings.TrimSpace(config.Resolution.EffectiveModel)
	}
	modelCategory := strings.TrimSpace(config.ModelCategory)
	if modelCategory == "" {
		modelCategory = strings.TrimSpace(config.Resolution.ModelCategory)
	}
	if model == "" {
		model = strings.TrimSpace(config.Model)
	}
	if model == "" {
		model = strings.TrimSpace(reference.Model)
	}
	if platform == "" || providerID == "" || model == "" {
		return petAIProviderRuntime{}, newPetProviderError(PET_PROVIDER_CONFIG_INVALID, reference, "provider 配置不完整", nil)
	}
	if runeLen(model) > PetAIMaxModelLength {
		return petAIProviderRuntime{}, newPetProviderError(PET_PROVIDER_CONFIG_INVALID, reference, "provider model 超出长度限制", nil)
	}

	protocol, err := normalizePetAIProtocol(platform, config.Protocol, config.UpstreamProtocol)
	if err != nil {
		return petAIProviderRuntime{}, newPetProviderError(PET_PROVIDER_PROTOCOL_INVALID, reference, "provider protocol 不支持", nil)
	}
	if capability == PetCapabilityTTS && protocol != "openai" {
		// chat-audio 和 speech 都是 OpenAI-compatible 形状；其它协议必须在
		// provider 解析阶段直接拒绝，不能把请求猜成另一个协议。
		return petAIProviderRuntime{}, newPetProviderError(PET_CAPABILITY_UNSUPPORTED, reference, "当前 provider 不支持语音能力", nil)
	}
	capabilitySupported := petCapabilitySupportedForModel(capability, model, modelCategory, modelCategory != "")
	if !capabilitySupported && capability == PetCapabilityTTS && modelCategory == "" {
		// 旧版自定义 reader 可能没有 modelCategories，但会通过 audioMode 明确声明
		// 语音端点。这里保留这条兼容事实，避免把一个同时承担聊天/TTS 桥接的
		// 自定义 provider 误判为不可用；一旦存在显式类别，类别校验仍然拥有最高优先级。
		capabilitySupported = isPetAISpeechAudioMode(config.AudioMode)
	}
	if !capabilitySupported {
		return petAIProviderRuntime{}, newPetProviderError(PET_CAPABILITY_UNSUPPORTED, reference, "model 不支持引用的宠物能力", nil)
	}

	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(config.APIURL)
	}
	headers := make(map[string]string, len(config.Headers))
	for key, value := range config.Headers {
		if strings.TrimSpace(key) == "" || hasLineBreak(key) || hasLineBreak(value) {
			return petAIProviderRuntime{}, newPetProviderError(PET_PROVIDER_CONFIG_INVALID, reference, "provider header 不安全", nil)
		}
		headers[key] = value
	}

	return petAIProviderRuntime{
		reference:       reference,
		platform:        platform,
		providerID:      providerID,
		model:           model,
		modelCategory:   modelCategory,
		baseURL:         baseURL,
		apiKey:          strings.TrimSpace(config.APIKey),
		apiEndpoint:     strings.TrimSpace(config.APIEndpoint),
		speechEndpoint:  strings.TrimSpace(config.SpeechEndpoint),
		protocol:        protocol,
		authType:        strings.ToLower(strings.TrimSpace(config.AuthType)),
		authHeader:      strings.TrimSpace(config.AuthHeader),
		headers:         headers,
		maxOutputTokens: config.MaxOutputTokens,
		audioMode:       strings.ToLower(strings.TrimSpace(config.AudioMode)),
	}, nil
}

func normalizePetAIProtocol(platform, protocol, upstreamProtocol string) (string, error) {
	raw := strings.ToLower(strings.TrimSpace(protocol))
	if raw == "" {
		raw = strings.ToLower(strings.TrimSpace(upstreamProtocol))
	}
	if raw == "" {
		switch platform {
		case "gemini", "google", "google-gemini":
			raw = "gemini"
		case "claude", "claude-code", "claude_code", "anthropic":
			raw = "anthropic"
		case "openai", "openai-compatible", "openai_compatible":
			raw = "openai"
		case "codex":
			// Codex 的空协议配置仍然必须落到 Responses；只有显式
			// openai_chat/openai 才允许调用旧的 Chat Completions 兼容层。
			raw = "responses"
		}
	}
	if raw == "auto" {
		// 正常 provider reader 会结合 APIEndpoint 先完成 auto 检测；这里仍
		// 保留运行时兜底，防止自定义 reader 把 auto 原样传入而让 Codex 误走 Chat。
		switch platform {
		case "codex":
			raw = "responses"
		case "claude", "claude-code", "claude_code", "anthropic":
			raw = "anthropic"
		default:
			raw = "openai"
		}
	}
	switch raw {
	case "openai", "openai-chat", "openai_chat", "openai-compatible", "openai_compatible":
		return "openai", nil
	case "responses", "openai-responses", "openai_responses", "codex":
		return "responses", nil
	case "anthropic", "anthropic-messages", "messages":
		return "anthropic", nil
	case "gemini", "google-gemini", "generate-content", "generatecontent":
		return "gemini", nil
	default:
		return "", errors.New("unsupported provider protocol")
	}
}

func (s *PetAIService) executeText(
	ctx context.Context,
	provider petAIProviderRuntime,
	input petAIChatInput,
	onDelta func(string) error,
	onUsage func(modelpricing.UsageSnapshot),
) (string, error) {
	if input.ProjectFolder != "" {
		return s.executeTextWithTools(ctx, provider, input, onDelta, onUsage)
	}
	return s.executeStreamingText(ctx, provider, input, onDelta, onUsage)
}

func (s *PetAIService) executeStreamingText(
	ctx context.Context,
	provider petAIProviderRuntime,
	input petAIChatInput,
	onDelta func(string) error,
	onUsage func(modelpricing.UsageSnapshot),
) (string, error) {
	var body []byte
	var endpoint string
	var accept string
	var err error
	switch provider.protocol {
	case "openai":
		body, err = buildOpenAIChatBody(provider, input)
		if err == nil {
			endpoint, err = providerEndpoint(provider, "chat")
		}
		accept = "text/event-stream"
	case "responses":
		body, err = buildPetAIResponsesBody(provider, input)
		if err == nil {
			endpoint, err = providerEndpoint(provider, "responses")
		}
		accept = "text/event-stream"
	case "anthropic":
		body, err = buildAnthropicMessagesBody(provider, input)
		if err == nil {
			endpoint, err = providerEndpoint(provider, "messages")
		}
		accept = "text/event-stream"
	case "gemini":
		body, err = buildGeminiGenerateContentBody(provider, input)
		if err == nil {
			endpoint, err = providerEndpoint(provider, "gemini")
		}
		accept = "application/json"
	default:
		return "", newPetProviderError(PET_PROVIDER_PROTOCOL_INVALID, provider.reference, "provider protocol 不支持", nil)
	}
	if err != nil {
		if _, ok := err.(*PetProviderError); ok {
			return "", err
		}
		return "", newPetProviderError(PET_PROVIDER_CONFIG_INVALID, provider.reference, "provider endpoint 无效", nil)
	}
	if int64(len(body)) > s.options.MaxRequestBytes {
		return "", newPetAIError(PET_AI_REQUEST_TOO_LARGE, 0, nil)
	}

	response, err := s.doJSONRequest(ctx, provider, endpoint, body, accept)
	if err != nil {
		return "", err
	}
	if response.Body == nil {
		return "", newPetAIError(PET_AI_RESPONSE_INVALID, response.StatusCode, nil)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", s.upstreamStatusError(response)
	}

	if provider.protocol == "gemini" {
		text, usage, err := parseGeminiTextWithUsage(response.Body, s.options.MaxResponseBytes)
		if err == nil && onUsage != nil {
			onUsage(usage)
		}
		return text, err
	}
	contentType := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Type")))
	if contentType != "" {
		mediaType, _, parseErr := mime.ParseMediaType(contentType)
		if parseErr != nil || mediaType != "text/event-stream" {
			return "", newPetAIError(PET_AI_RESPONSE_INVALID, response.StatusCode, nil)
		}
	}
	return parseTextSSE(response.Body, provider.protocol, s.options, onDelta, onUsage)
}

func (s *PetAIService) executeTextWithTools(
	ctx context.Context,
	provider petAIProviderRuntime,
	input petAIChatInput,
	onDelta func(string) error,
	onUsage func(modelpricing.UsageSnapshot),
) (string, error) {
	executor, err := NewPetAgentToolExecutor(input.ProjectFolder)
	if err != nil {
		// NewPetAgentToolExecutor 已负责目录存在性、根目录 symlink 和限制校验；
		// 这里只投影为稳定请求错误，不能把本机绝对路径或 OS 错误文案返回给前端。
		return "", newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}

	initial, err := s.executePetAIToolTurn(ctx, provider, input, nil, onUsage)
	if err != nil {
		return "", err
	}
	continuation := func(continuationCtx context.Context, request PetAgentContinuationRequest) (PetAgentAssistantTurn, error) {
		return s.executePetAIToolTurn(continuationCtx, provider, input, request.NativeMessages, onUsage)
	}
	coordinator := NewPetAgentToolLoopCoordinator(executor, continuation, PetAgentToolLoopOptions{
		Protocol:     petAgentProtocolForAI(provider.protocol),
		MaxRounds:    s.options.MaxToolContinuationRounds,
		MaxToolCalls: s.options.MaxToolCalls,
	})
	result, err := coordinator.Run(ctx, initial)
	if err != nil {
		return "", classifyPetAIToolLoopError(err, ctx)
	}
	text := strings.TrimSpace(result.Final.Text)
	if text == "" {
		return "", newPetAIError(PET_AI_RESPONSE_INVALID, 0, nil)
	}
	if onDelta != nil {
		if err := onDelta(text); err != nil {
			return "", err
		}
	}
	return text, nil
}

func (s *PetAIService) executePetAIToolTurn(
	ctx context.Context,
	provider petAIProviderRuntime,
	input petAIChatInput,
	nativeMessages json.RawMessage,
	onUsage func(modelpricing.UsageSnapshot),
) (PetAgentAssistantTurn, error) {
	body, endpoint, err := buildPetAIToolRequest(provider, input, nativeMessages)
	if err != nil {
		if PetProviderErrorCodeOf(err) != "" {
			return PetAgentAssistantTurn{}, err
		}
		return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, 0, nil)
	}
	if int64(len(body)) > s.options.MaxRequestBytes {
		return PetAgentAssistantTurn{}, newPetAIError(PET_AI_REQUEST_TOO_LARGE, 0, nil)
	}
	response, err := s.doJSONRequest(ctx, provider, endpoint, body, "application/json")
	if err != nil {
		return PetAgentAssistantTurn{}, err
	}
	if response.Body == nil {
		return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, response.StatusCode, nil)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return PetAgentAssistantTurn{}, s.upstreamStatusError(response)
	}
	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	if contentType != "" {
		mediaType, _, parseErr := mime.ParseMediaType(contentType)
		if parseErr != nil || mediaType != "application/json" {
			return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, response.StatusCode, nil)
		}
	}
	data, err := readLimitedBody(response.Body, s.options.MaxResponseBytes)
	if err != nil {
		return PetAgentAssistantTurn{}, err
	}
	turn, err := parsePetAIAssistantTurn(data, provider.protocol, response.StatusCode)
	if err == nil && onUsage != nil {
		if usage, ok := parsePetAIUsage(string(data), provider.protocol); ok {
			onUsage(usage)
		}
	}
	return turn, err
}

func petAgentProtocolForAI(protocol string) PetAgentToolProtocol {
	switch protocol {
	case "openai":
		return PetAgentProtocolOpenAI
	case "responses":
		return PetAgentProtocolResponses
	case "anthropic":
		return PetAgentProtocolAnthropic
	case "gemini":
		return PetAgentProtocolGemini
	default:
		return ""
	}
}

func classifyPetAIToolLoopError(err error, ctx context.Context) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return classifyPetAIContextError(ctx.Err())
	}
	if isPetAIError(err) || PetProviderErrorCodeOf(err) != "" {
		return err
	}
	// 达到 continuation 上限、执行器 workspace 异常或本地协议拼装失败都不能
	// 透传内部 error；统一压成响应无效，避免暴露路径、工具名或实现细节。
	return newPetAIError(PET_AI_RESPONSE_INVALID, 0, nil)
}

func (s *PetAIService) executeSpeech(
	ctx context.Context,
	provider petAIProviderRuntime,
	request PetSpeechRequest,
) ([]byte, string, error) {
	if provider.protocol != "openai" || isPetAIChatAudioMode(provider.audioMode) || strings.Contains(strings.ToLower(provider.speechEndpoint), "/chat/completions") {
		return nil, "", newPetProviderError(PET_CAPABILITY_UNSUPPORTED, provider.reference, "当前 provider 不支持 speech endpoint", nil)
	}

	bodyMap := map[string]any{
		"model": provider.model,
		"input": request.Text,
	}
	if request.Voice != "" {
		bodyMap["voice"] = request.Voice
	}
	if request.Instruction != "" {
		// OpenAI speech endpoint 的协议字段是 instructions（复数），不是 chat 的 message。
		bodyMap["instructions"] = request.Instruction
	}
	body, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, "", newPetAIError(PET_AI_REQUEST_TOO_LARGE, 0, nil)
	}
	if int64(len(body)) > s.options.MaxRequestBytes {
		return nil, "", newPetAIError(PET_AI_REQUEST_TOO_LARGE, 0, nil)
	}
	endpoint, err := providerEndpoint(provider, "speech")
	if err != nil {
		return nil, "", newPetProviderError(PET_PROVIDER_CONFIG_INVALID, provider.reference, "provider speech endpoint 无效", nil)
	}
	response, err := s.doJSONRequest(ctx, provider, endpoint, body, "audio/*")
	if err != nil {
		return nil, "", err
	}
	if response.Body == nil {
		return nil, "", newPetAIError(PET_AI_RESPONSE_INVALID, response.StatusCode, nil)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, "", s.upstreamStatusError(response)
	}
	audio, err := readLimitedBody(response.Body, s.options.MaxAudioBytes)
	if err != nil {
		return nil, "", err
	}
	if len(audio) == 0 {
		return nil, "", newPetAIError(PET_AI_RESPONSE_INVALID, response.StatusCode, nil)
	}
	mediaType := strings.TrimSpace(response.Header.Get("Content-Type"))
	if mediaType == "" {
		mediaType = "audio/mpeg"
	}
	format, err := ParsePetAudioMediaType(mediaType)
	if err != nil {
		return nil, "", newPetAIError(PET_AI_MEDIA_TYPE_INVALID, response.StatusCode, nil)
	}
	return audio, format.MediaType, nil
}

func isPetAIChatAudioMode(audioMode string) bool {
	switch strings.ToLower(strings.TrimSpace(audioMode)) {
	case "chat", "chat_audio", "chat-audio":
		return true
	default:
		return false
	}
}

func isPetAISpeechAudioMode(audioMode string) bool {
	switch strings.ToLower(strings.TrimSpace(audioMode)) {
	case "speech", "chat", "chat_audio", "chat-audio":
		return true
	default:
		return false
	}
}

func resolvePetSpeechMode(requestMode PetVoiceMode, provider petAIProviderRuntime) (PetVoiceMode, error) {
	if provider.protocol != "openai" {
		return "", newPetProviderError(PET_CAPABILITY_UNSUPPORTED, provider.reference, "当前 provider 不支持语音能力", nil)
	}
	if requestMode == PetVoiceSpeech || requestMode == PetVoiceChat {
		return requestMode, nil
	}
	if requestMode != PetVoiceAuto {
		return "", newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}

	// auto 先尊重 provider runtime 的显式 audioMode；只有 reader 没有提供模式时，
	// 才使用 model id 兼容规则，避免 provider 明确声明 speech 却被旧名称误判成 chat。
	switch audioMode := strings.ToLower(strings.TrimSpace(provider.audioMode)); audioMode {
	case "speech":
		return PetVoiceSpeech, nil
	case "chat", "chat_audio", "chat-audio":
		return PetVoiceChat, nil
	case "", "auto":
		model := strings.ToLower(strings.TrimSpace(provider.model))
		if strings.Contains(model, "mimo") || strings.Contains(model, "-audio") {
			return PetVoiceChat, nil
		}
		return PetVoiceSpeech, nil
	default:
		// 非空但未知的 provider audioMode 不是“未提供”，不能静默降级到任意 endpoint。
		return "", newPetProviderError(PET_PROVIDER_CONFIG_INVALID, provider.reference, "provider audio mode 无效", nil)
	}
}

func (s *PetAIService) executeChatAudio(
	ctx context.Context,
	provider petAIProviderRuntime,
	request PetSpeechRequest,
) ([]byte, string, error) {
	if provider.protocol != "openai" {
		return nil, "", newPetProviderError(PET_CAPABILITY_UNSUPPORTED, provider.reference, "当前 provider 不支持 chat audio", nil)
	}
	body, err := buildOpenAIChatAudioBody(provider, request)
	if err != nil {
		return nil, "", newPetAIError(PET_AI_REQUEST_TOO_LARGE, 0, nil)
	}
	if int64(len(body)) > s.options.MaxRequestBytes {
		return nil, "", newPetAIError(PET_AI_REQUEST_TOO_LARGE, 0, nil)
	}
	endpoint, err := providerEndpoint(provider, "chat")
	if err != nil {
		return nil, "", newPetProviderError(PET_PROVIDER_CONFIG_INVALID, provider.reference, "provider chat audio endpoint 无效", nil)
	}
	if isPetAISpeechEndpoint(endpoint) {
		// chat 模式必须留在 chat/completions；错误配置不能退回 speech endpoint。
		return nil, "", newPetProviderError(PET_CAPABILITY_UNSUPPORTED, provider.reference, "chat audio 不支持 speech endpoint", nil)
	}
	response, err := s.doJSONRequest(ctx, provider, endpoint, body, "application/json")
	if err != nil {
		return nil, "", err
	}
	if response.Body == nil {
		return nil, "", newPetAIError(PET_AI_RESPONSE_INVALID, response.StatusCode, nil)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, "", s.upstreamStatusError(response)
	}
	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	if contentType != "" {
		mediaType, _, parseErr := mime.ParseMediaType(contentType)
		if parseErr != nil || mediaType != "application/json" {
			return nil, "", newPetAIError(PET_AI_RESPONSE_INVALID, response.StatusCode, nil)
		}
	}
	data, err := readLimitedBody(response.Body, s.options.MaxResponseBytes)
	if err != nil {
		return nil, "", err
	}
	return parsePetChatAudioResponse(data, s.options.MaxAudioBytes, response.StatusCode)
}

func buildOpenAIChatAudioBody(provider petAIProviderRuntime, request PetSpeechRequest) ([]byte, error) {
	model := strings.ToLower(strings.TrimSpace(provider.model))
	messages := make([]map[string]any, 0, 2)
	if strings.Contains(model, "mimo") {
		if request.Instruction != "" {
			messages = append(messages, map[string]any{"role": "user", "content": request.Instruction})
		}
		messages = append(messages, map[string]any{
			"role":    "assistant",
			"content": applyPetVoiceTag(provider.model, request.VoiceTag, request.Text),
		})
	} else {
		directive := "Read the following text aloud exactly as written. Do not add, omit or change anything."
		if request.Instruction != "" {
			directive += " Speaking style: " + request.Instruction
		}
		messages = append(messages, map[string]any{
			"role":    "user",
			"content": directive + "\n\n" + request.Text,
		})
	}

	audio := map[string]any{"format": "wav"}
	if request.Voice != "" {
		audio["voice"] = request.Voice
	}
	return json.Marshal(map[string]any{
		"model":      provider.model,
		"modalities": []string{"text", "audio"},
		"messages":   messages,
		"audio":      audio,
		"stream":     false,
	})
}

func applyPetVoiceTag(model, tag, text string) string {
	if tag == "" || !strings.Contains(strings.ToLower(model), "mimo") {
		return text
	}
	// MiMo 将前置括号内容解释为方言/情绪指令；其它模型不能把 tag 当作隐式文本发送。
	tag = strings.Trim(tag, "（）()[]\\\"' \t")
	if tag == "" {
		return text
	}
	return "(" + tag + ")" + text
}

func isPetAISpeechEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	return err == nil && strings.HasSuffix(strings.ToLower(parsed.Path), "/audio/speech")
}

func parsePetChatAudioResponse(data []byte, maxAudioBytes int64, status int) ([]byte, string, error) {
	var response struct {
		Choices []struct {
			Message struct {
				Audio *struct {
					Data   *string `json:"data"`
					Format *string `json:"format"`
				} `json:"audio"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &response); err != nil || len(response.Choices) == 0 || response.Choices[0].Message.Audio == nil {
		return nil, "", newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
	}
	audio := response.Choices[0].Message.Audio
	if audio.Data == nil || *audio.Data == "" {
		return nil, "", newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
	}
	base64Data := *audio.Data
	if base64Data != strings.TrimSpace(base64Data) || strings.ContainsAny(base64Data, " \t\r\n") || strings.Contains(base64Data, ",") || strings.HasPrefix(strings.ToLower(base64Data), "data:") {
		return nil, "", newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
	}
	if maxAudioBytes <= 0 || int64(len(base64Data)) > ((maxAudioBytes+2)/3)*4 {
		return nil, "", newPetAIError(PET_AI_RESPONSE_TOO_LARGE, status, nil)
	}
	decoded, err := DecodePetAudioBase64(base64Data, maxAudioBytes)
	if err != nil {
		code := PetAIErrorCodeOf(err)
		if code == string(PET_AI_RESPONSE_TOO_LARGE) {
			return nil, "", newPetAIError(PET_AI_RESPONSE_TOO_LARGE, status, nil)
		}
		return nil, "", newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
	}
	format := ""
	if audio.Format != nil {
		format = *audio.Format
	}
	mediaType, ok := petChatAudioMediaType(format)
	if !ok {
		return nil, "", newPetAIError(PET_AI_MEDIA_TYPE_INVALID, status, nil)
	}
	return decoded, mediaType, nil
}

func petChatAudioMediaType(format string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "wav", "pcm", "pcm16":
		return "audio/wav", true
	case "mp3":
		return "audio/mpeg", true
	case "opus":
		return "audio/ogg", true
	case "aac":
		return "audio/aac", true
	case "flac":
		return "audio/flac", true
	default:
		return "", false
	}
}

type petAIToolRequestOptions struct {
	IncludeTools   bool
	Stream         bool
	NativeMessages json.RawMessage
}

func buildPetAIToolRequest(
	provider petAIProviderRuntime,
	input petAIChatInput,
	nativeMessages json.RawMessage,
) ([]byte, string, error) {
	options := petAIToolRequestOptions{
		IncludeTools:   true,
		Stream:         false,
		NativeMessages: nativeMessages,
	}
	switch provider.protocol {
	case "openai":
		body, err := buildOpenAIChatBodyWithOptions(provider, input, options)
		if err != nil {
			return nil, "", err
		}
		endpoint, err := providerEndpoint(provider, "chat")
		if err != nil {
			return nil, "", newPetProviderError(PET_PROVIDER_CONFIG_INVALID, provider.reference, "provider endpoint 无效", nil)
		}
		return body, endpoint, nil
	case "responses":
		body, err := buildPetAIResponsesBodyWithOptions(provider, input, options)
		if err != nil {
			return nil, "", err
		}
		endpoint, err := providerEndpoint(provider, "responses")
		if err != nil {
			return nil, "", newPetProviderError(PET_PROVIDER_CONFIG_INVALID, provider.reference, "provider endpoint 无效", nil)
		}
		return body, endpoint, nil
	case "anthropic":
		body, err := buildAnthropicMessagesBodyWithOptions(provider, input, options)
		if err != nil {
			return nil, "", err
		}
		endpoint, err := providerEndpoint(provider, "messages")
		if err != nil {
			return nil, "", newPetProviderError(PET_PROVIDER_CONFIG_INVALID, provider.reference, "provider endpoint 无效", nil)
		}
		return body, endpoint, nil
	case "gemini":
		body, err := buildGeminiGenerateContentBodyWithOptions(provider, input, options)
		if err != nil {
			return nil, "", err
		}
		endpoint, err := providerEndpoint(provider, "gemini")
		if err != nil {
			return nil, "", newPetProviderError(PET_PROVIDER_CONFIG_INVALID, provider.reference, "provider endpoint 无效", nil)
		}
		return body, endpoint, nil
	default:
		return nil, "", newPetProviderError(PET_PROVIDER_PROTOCOL_INVALID, provider.reference, "provider protocol 不支持", nil)
	}
}

func buildOpenAIChatBody(provider petAIProviderRuntime, input petAIChatInput) ([]byte, error) {
	return buildOpenAIChatBodyWithOptions(provider, input, petAIToolRequestOptions{Stream: true})
}

func buildOpenAIChatBodyWithOptions(
	provider petAIProviderRuntime,
	input petAIChatInput,
	options petAIToolRequestOptions,
) ([]byte, error) {
	messages := make([]map[string]any, 0, len(input.History)+2)
	if input.Persona != "" {
		messages = append(messages, map[string]any{"role": "system", "content": input.Persona})
	}
	for _, item := range input.History {
		messages = append(messages, map[string]any{"role": item.Role, "content": petAIOpenAIContent(item.Content, item.Images)})
	}
	messages = append(messages, map[string]any{"role": "user", "content": petAIOpenAIContent(input.UserText, input.Images)})
	var err error
	messages, err = appendPetAINativeMessages(messages, options.NativeMessages)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"model":    provider.model,
		"messages": messages,
		"stream":   options.Stream,
	}
	if options.Stream {
		// OpenAI-compatible providers only return the final usage chunk when this
		// option is enabled; without it the stream can complete successfully while
		// the pet ledger has no authoritative input/output token fact.
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	if options.IncludeTools {
		body["tools"] = petAIOpenAITools()
	}
	if input.Reasoning != "" && input.Reasoning != "none" {
		body["reasoning_effort"] = input.Reasoning
	}
	return json.Marshal(body)
}

func buildPetAIResponsesBody(provider petAIProviderRuntime, input petAIChatInput) ([]byte, error) {
	return buildPetAIResponsesBodyWithOptions(provider, input, petAIToolRequestOptions{Stream: true})
}

func buildPetAIResponsesBodyWithOptions(
	provider petAIProviderRuntime,
	input petAIChatInput,
	options petAIToolRequestOptions,
) ([]byte, error) {
	items := make([]map[string]any, 0, len(input.History)+2)
	if input.Persona != "" {
		items = append(items, petAIResponsesMessage("system", input.Persona, nil))
	}
	for _, item := range input.History {
		items = append(items, petAIResponsesMessage(item.Role, item.Content, item.Images))
	}
	items = append(items, petAIResponsesMessage("user", input.UserText, input.Images))
	var err error
	items, err = appendPetAINativeMessages(items, options.NativeMessages)
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"model":  provider.model,
		"input":  items,
		"stream": options.Stream,
	}
	if options.IncludeTools {
		body["tools"] = petAIResponsesTools()
	}
	if provider.maxOutputTokens > 0 {
		body["max_output_tokens"] = provider.maxOutputTokens
	}
	if input.Reasoning != "" && input.Reasoning != "none" {
		body["reasoning"] = map[string]string{"effort": input.Reasoning}
	}
	return json.Marshal(body)
}

func petAIResponsesMessage(role, text string, images []PetAIImage) map[string]any {
	if len(images) == 0 {
		return map[string]any{"role": role, "content": text}
	}
	parts := make([]map[string]any, 0, len(images)+1)
	if text != "" {
		textType := "input_text"
		if role == "assistant" {
			textType = "output_text"
		}
		parts = append(parts, map[string]any{"type": textType, "text": text})
	}
	for _, image := range images {
		parts = append(parts, map[string]any{
			"type":      "input_image",
			"image_url": "data:" + image.MediaType + ";base64," + image.Data,
		})
	}
	return map[string]any{"role": role, "content": parts}
}

func buildAnthropicMessagesBody(provider petAIProviderRuntime, input petAIChatInput) ([]byte, error) {
	return buildAnthropicMessagesBodyWithOptions(provider, input, petAIToolRequestOptions{Stream: true})
}

func buildAnthropicMessagesBodyWithOptions(
	provider petAIProviderRuntime,
	input petAIChatInput,
	options petAIToolRequestOptions,
) ([]byte, error) {
	messages := make([]map[string]any, 0, len(input.History)+1)
	for _, item := range input.History {
		messages = append(messages, map[string]any{"role": item.Role, "content": petAIAnthropicContent(item.Content, item.Images)})
	}
	messages = append(messages, map[string]any{"role": "user", "content": petAIAnthropicContent(input.UserText, input.Images)})
	var err error
	messages, err = appendPetAINativeMessages(messages, options.NativeMessages)
	if err != nil {
		return nil, err
	}
	maxTokens := provider.maxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	body := map[string]any{
		"model":      provider.model,
		"max_tokens": maxTokens,
		"messages":   messages,
		"stream":     options.Stream,
	}
	if options.IncludeTools {
		body["tools"] = petAIAnthropicTools()
	}
	if input.Persona != "" {
		body["system"] = input.Persona
	}
	if input.Reasoning != "" && input.Reasoning != "none" {
		budget := petAIReasoningBudget(input.Reasoning)
		if maxTokens <= budget {
			body["max_tokens"] = budget + 256
		}
		body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	}
	return json.Marshal(body)
}

func buildGeminiGenerateContentBody(provider petAIProviderRuntime, input petAIChatInput) ([]byte, error) {
	return buildGeminiGenerateContentBodyWithOptions(provider, input, petAIToolRequestOptions{})
}

func buildGeminiGenerateContentBodyWithOptions(
	provider petAIProviderRuntime,
	input petAIChatInput,
	options petAIToolRequestOptions,
) ([]byte, error) {
	contents := make([]map[string]any, 0, len(input.History)+1)
	for _, item := range input.History {
		role := item.Role
		if role == "assistant" {
			role = "model"
		}
		contents = append(contents, map[string]any{
			"role":  role,
			"parts": petAIGeminiParts(item.Content, item.Images),
		})
	}
	contents = append(contents, map[string]any{
		"role":  "user",
		"parts": petAIGeminiParts(input.UserText, input.Images),
	})
	var err error
	contents, err = appendPetAINativeMessages(contents, options.NativeMessages)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"contents": contents}
	if options.IncludeTools {
		body["tools"] = petAIGeminiTools()
	}
	if input.Persona != "" {
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]string{{"text": input.Persona}},
		}
	}
	if input.Reasoning != "" && input.Reasoning != "none" {
		body["generationConfig"] = map[string]any{
			"thinkingConfig": map[string]int{"thinkingBudget": petAIReasoningBudget(input.Reasoning)},
		}
	}
	return json.Marshal(body)
}

func appendPetAINativeMessages(messages []map[string]any, native json.RawMessage) ([]map[string]any, error) {
	if len(bytes.TrimSpace(native)) == 0 {
		return messages, nil
	}
	var continuation []map[string]any
	if err := json.Unmarshal(native, &continuation); err != nil || len(continuation) == 0 {
		return nil, errors.New("invalid provider continuation messages")
	}
	return append(messages, continuation...), nil
}

func petAIOpenAITools() []map[string]any {
	definitions := PetAgentToolDefinitions()
	tools := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        string(definition.Name),
				"description": definition.Description,
				"parameters":  definition.InputSchema,
			},
		})
	}
	return tools
}

func petAIResponsesTools() []map[string]any {
	definitions := PetAgentToolDefinitions()
	tools := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, map[string]any{
			"type":        "function",
			"name":        string(definition.Name),
			"description": definition.Description,
			"parameters":  definition.InputSchema,
		})
	}
	return tools
}

func petAIAnthropicTools() []map[string]any {
	definitions := PetAgentToolDefinitions()
	tools := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, map[string]any{
			"name":         string(definition.Name),
			"description":  definition.Description,
			"input_schema": definition.InputSchema,
		})
	}
	return tools
}

func petAIGeminiTools() []map[string]any {
	definitions := PetAgentToolDefinitions()
	declarations := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		declarations = append(declarations, map[string]any{
			"name":        string(definition.Name),
			"description": definition.Description,
			"parameters":  definition.InputSchema,
		})
	}
	return []map[string]any{{"functionDeclarations": declarations}}
}

func petAIOpenAIContent(text string, images []PetAIImage) any {
	if len(images) == 0 {
		return text
	}
	parts := make([]map[string]any, 0, len(images)+1)
	if text != "" {
		parts = append(parts, map[string]any{"type": "text", "text": text})
	}
	for _, image := range images {
		parts = append(parts, map[string]any{
			"type": "image_url",
			"image_url": map[string]string{
				"url": "data:" + image.MediaType + ";base64," + image.Data,
			},
		})
	}
	return parts
}

func petAIAnthropicContent(text string, images []PetAIImage) any {
	if len(images) == 0 {
		return text
	}
	parts := make([]map[string]any, 0, len(images)+1)
	if text != "" {
		parts = append(parts, map[string]any{"type": "text", "text": text})
	}
	for _, image := range images {
		parts = append(parts, map[string]any{
			"type": "image",
			"source": map[string]string{
				"type":       "base64",
				"media_type": image.MediaType,
				"data":       image.Data,
			},
		})
	}
	return parts
}

func petAIGeminiParts(text string, images []PetAIImage) []map[string]any {
	parts := make([]map[string]any, 0, len(images)+1)
	if text != "" {
		parts = append(parts, map[string]any{"text": text})
	}
	for _, image := range images {
		parts = append(parts, map[string]any{
			"inline_data": map[string]string{
				"mime_type": image.MediaType,
				"data":      image.Data,
			},
		})
	}
	return parts
}

func petAIReasoningBudget(reasoning string) int {
	switch reasoning {
	case "minimal":
		return 256
	case "low":
		return 1024
	case "medium":
		return 2048
	case "high":
		return 4096
	default:
		return 0
	}
}

func (s *PetAIService) doJSONRequest(
	ctx context.Context,
	provider petAIProviderRuntime,
	endpoint string,
	body []byte,
	accept string,
) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	request.Header.Set("Content-Type", "application/json")
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	for key, value := range provider.headers {
		request.Header.Set(key, value)
	}
	applyProviderAuth(request, provider)
	response, err := s.transport.RoundTrip(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, newPetAIError(PET_AI_UPSTREAM_ERROR, 0, nil)
	}
	if response == nil {
		return nil, newPetAIError(PET_AI_RESPONSE_INVALID, 0, nil)
	}
	return response, nil
}

func applyProviderAuth(request *http.Request, provider petAIProviderRuntime) {
	if provider.apiKey == "" {
		return
	}
	authType := provider.authType
	switch authType {
	case "x-api-key", "x_api_key":
		request.Header.Set("x-api-key", provider.apiKey)
	case "api-key", "api_key":
		request.Header.Set("api-key", provider.apiKey)
	case "custom":
		if provider.authHeader != "" {
			request.Header.Set(provider.authHeader, provider.apiKey)
		}
	case "bearer", "":
		if provider.protocol == "anthropic" {
			request.Header.Set("x-api-key", provider.apiKey)
		} else if provider.protocol == "gemini" {
			request.Header.Set("x-goog-api-key", provider.apiKey)
		} else {
			request.Header.Set("Authorization", "Bearer "+provider.apiKey)
		}
	}
	if provider.protocol == "anthropic" && request.Header.Get("anthropic-version") == "" {
		request.Header.Set("anthropic-version", "2023-06-01")
	}
	if provider.protocol == "gemini" && request.Header.Get("x-goog-api-key") == "" && provider.authType == "x-api-key" {
		request.Header.Set("x-goog-api-key", provider.apiKey)
	}
}

func providerEndpoint(provider petAIProviderRuntime, kind string) (string, error) {
	base := strings.TrimSpace(provider.baseURL)
	if base == "" {
		switch provider.protocol {
		case "openai":
			base = "https://api.openai.com"
		case "anthropic":
			base = "https://api.anthropic.com"
		case "gemini":
			base = "https://generativelanguage.googleapis.com"
		}
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid provider base url")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("unsupported provider url scheme")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")

	override := ""
	switch kind {
	case "speech":
		override = provider.speechEndpoint
	case "chat", "responses", "messages", "gemini":
		override = provider.apiEndpoint
	case "transcription":
		// 转写默认沿用 provider base URL 的 OpenAI-compatible 路径；TTS 的
		// speechEndpoint 可能专门指向 /audio/speech，不能把它误用于输入转写。
	}
	if override != "" {
		path, err := joinPetAIEndpointPath(parsed.Path, override)
		if err != nil {
			return "", err
		}
		parsed.Path = path
		parsed.RawPath = ""
		return parsed.String(), nil
	}

	var route string
	switch kind {
	case "chat":
		route = "chat/completions"
	case "responses":
		route = "responses"
	case "messages":
		route = "messages"
	case "speech":
		route = "audio/speech"
	case "transcription":
		route = "audio/transcriptions"
	case "gemini":
		route = "models/" + url.PathEscape(provider.model) + ":generateContent"
	default:
		return "", errors.New("unsupported provider endpoint kind")
	}
	parsed.Path = defaultPetAIEndpointPath(parsed.Path, provider.protocol, route)
	parsed.RawPath = ""
	return parsed.String(), nil
}

func joinPetAIEndpointPath(basePath, endpoint string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("provider endpoint must be a relative path")
	}
	path := strings.TrimSpace(parsed.Path)
	if path == "" || hasLineBreak(path) {
		return "", errors.New("provider endpoint is empty")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/v1beta/") || basePath == "" {
		return path, nil
	}
	return strings.TrimRight(basePath, "/") + path, nil
}

func defaultPetAIEndpointPath(basePath, protocol, route string) string {
	basePath = strings.TrimRight(basePath, "/")
	lower := strings.ToLower(basePath)
	if strings.HasSuffix(lower, "/chat/completions") || strings.HasSuffix(lower, "/responses") || strings.HasSuffix(lower, "/messages") || strings.HasSuffix(lower, "/audio/speech") || strings.HasSuffix(lower, ":generatecontent") {
		return basePath
	}
	if strings.HasSuffix(lower, "/v1") || strings.HasSuffix(lower, "/v1beta") {
		return basePath + "/" + route
	}
	version := "v1"
	if protocol == "gemini" {
		version = "v1beta"
	}
	return basePath + "/" + version + "/" + route
}

func parseGeminiText(body io.Reader, maxBytes int64) (string, error) {
	text, _, err := parseGeminiTextWithUsage(body, maxBytes)
	return text, err
}

func parseGeminiTextWithUsage(body io.Reader, maxBytes int64) (string, modelpricing.UsageSnapshot, error) {
	data, err := readLimitedBody(body, maxBytes)
	if err != nil {
		return "", modelpricing.UsageSnapshot{}, err
	}
	var response struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought,omitempty"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata *struct {
			PromptTokenCount        int `json:"promptTokenCount"`
			CandidatesTokenCount    int `json:"candidatesTokenCount"`
			ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
			CachedContentTokenCount int `json:"cachedContentTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return "", modelpricing.UsageSnapshot{}, newPetAIError(PET_AI_RESPONSE_INVALID, 0, nil)
	}
	var text strings.Builder
	for _, candidate := range response.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.Thought {
				continue
			}
			text.WriteString(part.Text)
		}
	}
	result := strings.TrimSpace(text.String())
	if result == "" {
		return "", modelpricing.UsageSnapshot{}, newPetAIError(PET_AI_RESPONSE_INVALID, 0, nil)
	}
	usage := modelpricing.UsageSnapshot{}
	if response.UsageMetadata != nil {
		usage = modelpricing.UsageSnapshot{
			InputTokens:     response.UsageMetadata.PromptTokenCount,
			OutputTokens:    response.UsageMetadata.CandidatesTokenCount,
			ReasoningTokens: response.UsageMetadata.ThoughtsTokenCount,
			CacheReadTokens: response.UsageMetadata.CachedContentTokenCount,
		}
	}
	return result, usage, nil
}

// parsePetAIUsage 只读取 provider 明确回传的 usage 事实，不根据文本长度、请求体
// 或模型价格推算 token。不同协议的 usage 出现位置不同，这里统一投影到 canonical
// UsageSnapshot，后续价格和 premium 仍由 PetService 的唯一入账入口处理。
func parsePetAIUsage(data string, protocol string) (modelpricing.UsageSnapshot, bool) {
	var envelope struct {
		Usage   json.RawMessage `json:"usage"`
		Message struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"message"`
		Response struct {
			Usage       json.RawMessage `json:"usage"`
			ServiceTier string          `json:"service_tier"`
		} `json:"response"`
		UsageMetadata json.RawMessage `json:"usageMetadata"`
		ServiceTier   string          `json:"service_tier"`
	}
	if err := json.Unmarshal([]byte(data), &envelope); err != nil {
		return modelpricing.UsageSnapshot{}, false
	}

	usageData := envelope.Usage
	if protocol == "anthropic" && len(bytes.TrimSpace(usageData)) == 0 {
		// message_start 把 usage 放在 message.usage，message_delta 则直接放在
		// usage；两种形状都属于同一个上游请求，交给 merge 统一去重。
		usageData = envelope.Message.Usage
	}
	if protocol == "gemini" {
		usageData = envelope.UsageMetadata
	}
	if protocol == "responses" {
		// Responses API 的流式事件把累计 usage 放在 response.usage，
		// 非流响应则通常直接放在 usage；两种形状都交给同一套 max merge。
		trimmedUsage := bytes.TrimSpace(usageData)
		if len(trimmedUsage) == 0 || bytes.Equal(trimmedUsage, []byte("null")) {
			usageData = envelope.Response.Usage
		}
	}
	if len(bytes.TrimSpace(usageData)) == 0 || bytes.Equal(bytes.TrimSpace(usageData), []byte("null")) {
		return modelpricing.UsageSnapshot{}, false
	}

	var usage modelpricing.UsageSnapshot
	switch protocol {
	case "openai":
		var value struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			PromptDetails    struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
			ServiceTier string `json:"service_tier"`
		}
		if err := json.Unmarshal(usageData, &value); err != nil {
			return modelpricing.UsageSnapshot{}, false
		}
		usage = modelpricing.UsageSnapshot{
			InputTokens:     value.PromptTokens,
			OutputTokens:    value.CompletionTokens,
			ReasoningTokens: value.CompletionDetails.ReasoningTokens,
			CacheReadTokens: value.PromptDetails.CachedTokens,
			ServiceTier:     modelpricing.NormalizeObservedServiceTier(firstNonEmptyPetAIString(value.ServiceTier, envelope.ServiceTier), nil),
		}
	case "responses":
		var value struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			InputDetails struct {
				CachedTokens     int `json:"cached_tokens"`
				CacheWriteTokens int `json:"cache_write_tokens"`
			} `json:"input_tokens_details"`
			OutputDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
			ServiceTier string `json:"service_tier"`
		}
		if err := json.Unmarshal(usageData, &value); err != nil {
			return modelpricing.UsageSnapshot{}, false
		}
		usage = modelpricing.UsageSnapshot{
			InputTokens:       value.InputTokens,
			OutputTokens:      value.OutputTokens,
			ReasoningTokens:   value.OutputDetails.ReasoningTokens,
			CacheCreateTokens: value.InputDetails.CacheWriteTokens,
			CacheReadTokens:   value.InputDetails.CachedTokens,
			ServiceTier: modelpricing.NormalizeObservedServiceTier(
				firstNonEmptyPetAIString(value.ServiceTier, envelope.Response.ServiceTier, envelope.ServiceTier), nil,
			),
		}
	case "anthropic":
		var value struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreation            *struct {
				Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens"`
				Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
			ServiceTier string `json:"service_tier"`
		}
		if err := json.Unmarshal(usageData, &value); err != nil {
			return modelpricing.UsageSnapshot{}, false
		}
		usage = modelpricing.UsageSnapshot{
			InputTokens:       value.InputTokens,
			OutputTokens:      value.OutputTokens,
			CacheCreateTokens: value.CacheCreationInputTokens,
			CacheReadTokens:   value.CacheReadInputTokens,
			ServiceTier:       modelpricing.NormalizeObservedServiceTier(firstNonEmptyPetAIString(value.ServiceTier, envelope.ServiceTier), nil),
		}
		if value.CacheCreation != nil {
			usage.CacheCreation = &modelpricing.CacheCreationDetail{
				Ephemeral5mTokens: value.CacheCreation.Ephemeral5mInputTokens,
				Ephemeral1hTokens: value.CacheCreation.Ephemeral1hInputTokens,
			}
		}
	case "gemini":
		var value struct {
			PromptTokenCount        int `json:"promptTokenCount"`
			CandidatesTokenCount    int `json:"candidatesTokenCount"`
			ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
			CachedContentTokenCount int `json:"cachedContentTokenCount"`
		}
		if err := json.Unmarshal(usageData, &value); err != nil {
			return modelpricing.UsageSnapshot{}, false
		}
		usage = modelpricing.UsageSnapshot{
			InputTokens:     value.PromptTokenCount,
			OutputTokens:    value.CandidatesTokenCount,
			ReasoningTokens: value.ThoughtsTokenCount,
			CacheReadTokens: value.CachedContentTokenCount,
		}
	default:
		return modelpricing.UsageSnapshot{}, false
	}
	if hasInvalidPetAIUsage(usage) {
		return modelpricing.UsageSnapshot{}, false
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.ReasoningTokens == 0 &&
		usage.CacheCreateTokens == 0 && usage.CacheReadTokens == 0 &&
		(usage.CacheCreation == nil || (usage.CacheCreation.Ephemeral5mTokens == 0 && usage.CacheCreation.Ephemeral1hTokens == 0)) {
		return modelpricing.UsageSnapshot{}, false
	}
	return usage, true
}

// mergePetAIUsage 按字段取最大值，而不是累加 provider 重复回传的快照。
// OpenAI 可能在结束 chunk 重复完整 usage，Anthropic 也会把同一请求的部分
// usage 分散在多个事件；max 能保留已观察到的最终值并避免一次请求重复入账。
func mergePetAIUsage(current, next modelpricing.UsageSnapshot) modelpricing.UsageSnapshot {
	merged := current
	merged.InputTokens = maxPetAIUsageValue(current.InputTokens, next.InputTokens)
	merged.OutputTokens = maxPetAIUsageValue(current.OutputTokens, next.OutputTokens)
	merged.ReasoningTokens = maxPetAIUsageValue(current.ReasoningTokens, next.ReasoningTokens)
	merged.CacheCreateTokens = maxPetAIUsageValue(current.CacheCreateTokens, next.CacheCreateTokens)
	merged.CacheReadTokens = maxPetAIUsageValue(current.CacheReadTokens, next.CacheReadTokens)
	if current.CacheCreation == nil && next.CacheCreation != nil {
		merged.CacheCreation = &modelpricing.CacheCreationDetail{}
	}
	if merged.CacheCreation != nil {
		if current.CacheCreation != nil {
			merged.CacheCreation.Ephemeral5mTokens = current.CacheCreation.Ephemeral5mTokens
			merged.CacheCreation.Ephemeral1hTokens = current.CacheCreation.Ephemeral1hTokens
		}
		if next.CacheCreation != nil {
			merged.CacheCreation.Ephemeral5mTokens = maxPetAIUsageValue(merged.CacheCreation.Ephemeral5mTokens, next.CacheCreation.Ephemeral5mTokens)
			merged.CacheCreation.Ephemeral1hTokens = maxPetAIUsageValue(merged.CacheCreation.Ephemeral1hTokens, next.CacheCreation.Ephemeral1hTokens)
		}
	}
	if next.ServiceTier != modelpricing.ServiceTierDefault {
		merged.ServiceTier = next.ServiceTier
	}
	return merged
}

func petAIUsagePayload(requestID string, provider petAIProviderRuntime, usage modelpricing.UsageSnapshot) *PetStreamUsagePayload {
	providerName := strings.TrimSpace(provider.platform)
	providerID := strings.TrimSpace(provider.providerID)
	if providerName != "" && providerID != "" {
		providerName += "/" + providerID
	} else if providerName == "" {
		providerName = providerID
	}
	if providerName == "" {
		providerName = strings.TrimSpace(provider.reference.Platform)
		if providerName == "" {
			providerName = strings.TrimSpace(provider.reference.ProviderID)
		}
	}
	model := strings.TrimSpace(provider.model)
	if model == "" {
		model = strings.TrimSpace(provider.reference.Model)
	}
	payload := &PetStreamUsagePayload{
		ID:                strings.TrimSpace(requestID),
		At:                time.Now().UnixMilli(),
		Provider:          providerName,
		Model:             model,
		InputTokens:       usage.InputTokens,
		OutputTokens:      usage.OutputTokens,
		ReasoningTokens:   usage.ReasoningTokens,
		CacheCreateTokens: usage.CacheCreateTokens,
		CacheReadTokens:   usage.CacheReadTokens,
		ServiceTier:       string(usage.ServiceTier),
	}
	if usage.CacheCreation != nil {
		payload.Ephemeral5mTokens = usage.CacheCreation.Ephemeral5mTokens
		payload.Ephemeral1hTokens = usage.CacheCreation.Ephemeral1hTokens
	}
	return payload
}

func firstNonEmptyPetAIString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func maxPetAIUsageValue(current, next int) int {
	if next > current {
		return next
	}
	return current
}

func hasInvalidPetAIUsage(usage modelpricing.UsageSnapshot) bool {
	return usage.InputTokens < 0 ||
		usage.OutputTokens < 0 ||
		usage.ReasoningTokens < 0 ||
		usage.CacheCreateTokens < 0 ||
		usage.CacheReadTokens < 0 ||
		(usage.CacheCreation != nil &&
			(usage.CacheCreation.Ephemeral5mTokens < 0 || usage.CacheCreation.Ephemeral1hTokens < 0))
}

func parsePetAIAssistantTurn(data []byte, protocol string, status int) (PetAgentAssistantTurn, error) {
	switch protocol {
	case "openai":
		return parsePetAIOpenAIAssistantTurn(data, status)
	case "responses":
		return parsePetAIResponsesAssistantTurn(data, status)
	case "anthropic":
		return parsePetAIAnthropicAssistantTurn(data, status)
	case "gemini":
		return parsePetAIGeminiAssistantTurn(data, status)
	default:
		return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
	}
}

func parsePetAIOpenAIAssistantTurn(data []byte, status int) (PetAgentAssistantTurn, error) {
	var response struct {
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Message struct {
				Content   json.RawMessage `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
	}
	if petAIResponseHasError(response.Error) {
		return PetAgentAssistantTurn{}, newPetAIError(PET_AI_UPSTREAM_ERROR, status, nil)
	}
	if len(response.Choices) == 0 {
		return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
	}
	text, err := parsePetAITextContent(response.Choices[0].Message.Content)
	if err != nil {
		return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
	}
	turn := PetAgentAssistantTurn{Text: text}
	seenIDs := make(map[string]struct{}, len(response.Choices[0].Message.ToolCalls))
	for _, toolCall := range response.Choices[0].Message.ToolCalls {
		id := strings.TrimSpace(toolCall.ID)
		name := strings.TrimSpace(toolCall.Function.Name)
		if id == "" || name == "" || (toolCall.Type != "" && toolCall.Type != "function") {
			return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
		}
		if _, exists := seenIDs[id]; exists {
			return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
		}
		seenIDs[id] = struct{}{}
		arguments, err := normalizePetAIOpenAIArguments(toolCall.Function.Arguments)
		if err != nil {
			return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
		}
		turn.ToolCalls = append(turn.ToolCalls, PetAgentToolCall{
			ID:        id,
			Name:      PetAgentToolName(name),
			Arguments: arguments,
		})
	}
	if strings.TrimSpace(turn.Text) == "" && len(turn.ToolCalls) == 0 {
		return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
	}
	return turn, nil
}

func parsePetAIResponsesAssistantTurn(data []byte, status int) (PetAgentAssistantTurn, error) {
	var response struct {
		Error      json.RawMessage `json:"error"`
		OutputText string          `json:"output_text"`
		Output     []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			CallID    string          `json:"call_id"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
	}
	if petAIResponseHasError(response.Error) {
		return PetAgentAssistantTurn{}, newPetAIError(PET_AI_UPSTREAM_ERROR, status, nil)
	}

	turn := PetAgentAssistantTurn{}
	seenIDs := make(map[string]struct{}, len(response.Output))
	for _, item := range response.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Type == "" || part.Type == "output_text" || part.Type == "text" {
					turn.Text += part.Text
				}
			}
		case "function_call":
			callID := strings.TrimSpace(item.CallID)
			name := strings.TrimSpace(item.Name)
			if callID == "" || name == "" {
				return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
			}
			if _, exists := seenIDs[callID]; exists {
				return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
			}
			seenIDs[callID] = struct{}{}
			arguments, err := normalizePetAIResponsesArguments(item.Arguments)
			if err != nil {
				return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
			}
			turn.ToolCalls = append(turn.ToolCalls, PetAgentToolCall{
				ID:        callID,
				Name:      PetAgentToolName(name),
				Arguments: arguments,
			})
		}
	}
	if strings.TrimSpace(turn.Text) == "" {
		turn.Text = response.OutputText
	}
	if strings.TrimSpace(turn.Text) == "" && len(turn.ToolCalls) == 0 {
		return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
	}
	return turn, nil
}

func parsePetAIAnthropicAssistantTurn(data []byte, status int) (PetAgentAssistantTurn, error) {
	var response struct {
		Error   json.RawMessage `json:"error"`
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
	}
	if petAIResponseHasError(response.Error) {
		return PetAgentAssistantTurn{}, newPetAIError(PET_AI_UPSTREAM_ERROR, status, nil)
	}
	turn := PetAgentAssistantTurn{}
	seenIDs := make(map[string]struct{}, len(response.Content))
	for _, block := range response.Content {
		switch block.Type {
		case "text":
			turn.Text += block.Text
		case "tool_use":
			id := strings.TrimSpace(block.ID)
			name := strings.TrimSpace(block.Name)
			if id == "" || name == "" {
				return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
			}
			if _, exists := seenIDs[id]; exists {
				return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
			}
			seenIDs[id] = struct{}{}
			arguments, err := normalizePetAIObjectArguments(block.Input)
			if err != nil {
				return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
			}
			turn.ToolCalls = append(turn.ToolCalls, PetAgentToolCall{
				ID:        id,
				Name:      PetAgentToolName(name),
				Arguments: arguments,
			})
		}
	}
	if strings.TrimSpace(turn.Text) == "" && len(turn.ToolCalls) == 0 {
		return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
	}
	return turn, nil
}

func parsePetAIGeminiAssistantTurn(data []byte, status int) (PetAgentAssistantTurn, error) {
	var response struct {
		Error      json.RawMessage `json:"error"`
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string `json:"text"`
					Thought      bool   `json:"thought"`
					FunctionCall *struct {
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					} `json:"functionCall"`
					FunctionCallSnake *struct {
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					} `json:"function_call"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
	}
	if petAIResponseHasError(response.Error) {
		return PetAgentAssistantTurn{}, newPetAIError(PET_AI_UPSTREAM_ERROR, status, nil)
	}
	if len(response.Candidates) == 0 {
		return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
	}
	turn := PetAgentAssistantTurn{}
	callIndex := 0
	for _, candidate := range response.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.Text != "" && !part.Thought {
				turn.Text += part.Text
			}
			functionCall := part.FunctionCall
			if functionCall == nil {
				functionCall = part.FunctionCallSnake
			}
			if functionCall == nil {
				continue
			}
			name := strings.TrimSpace(functionCall.Name)
			if name == "" {
				return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
			}
			arguments, err := normalizePetAIObjectArguments(functionCall.Args)
			if err != nil {
				return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
			}
			callIndex++
			// Gemini functionCall 没有 OpenAI/Anthropic 风格的 id；这里生成仅供
			// 本地 coordinator 对齐结果的稳定 id，续接载荷仍按 Gemini 原生字段发送。
			turn.ToolCalls = append(turn.ToolCalls, PetAgentToolCall{
				ID:        "gemini_" + strconv.Itoa(callIndex),
				Name:      PetAgentToolName(name),
				Arguments: arguments,
			})
		}
	}
	if strings.TrimSpace(turn.Text) == "" && len(turn.ToolCalls) == 0 {
		return PetAgentAssistantTurn{}, newPetAIError(PET_AI_RESPONSE_INVALID, status, nil)
	}
	return turn, nil
}

func petAIResponseHasError(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func parsePetAITextContent(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return "", err
	}
	var builder strings.Builder
	for _, part := range parts {
		if part.Type == "" || part.Type == "text" {
			builder.WriteString(part.Text)
		}
	}
	return builder.String(), nil
}

func normalizePetAIOpenAIArguments(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage(`{}`), nil
	}
	var arguments string
	if err := json.Unmarshal(trimmed, &arguments); err != nil {
		return nil, err
	}
	return ParsePetAgentOpenAIArguments(arguments)
}

func normalizePetAIResponsesArguments(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage(`{}`), nil
	}
	// Responses function_call.arguments 按协议是 JSON 字符串；保留 object
	// 形状作为兼容输入，避免 relay 或第三方 Codex 代理把同一字段解码一次后
	// 让工具调用无故失败。
	var arguments string
	if err := json.Unmarshal(trimmed, &arguments); err == nil {
		return ParsePetAgentOpenAIArguments(arguments)
	}
	return normalizePetAIObjectArguments(trimmed)
}

func normalizePetAIObjectArguments(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return ParsePetAgentToolCallArguments(trimmed)
}

type petAISSEEvent struct {
	Event string
	Data  string
}

var errPetAISSEDone = errors.New("pet ai sse done")

func parseTextSSE(
	body io.Reader,
	protocol string,
	options PetAIOptions,
	onDelta func(string) error,
	onUsage func(modelpricing.UsageSnapshot),
) (string, error) {
	var text strings.Builder
	done := false
	consume := func(event petAISSEEvent) error {
		data := strings.TrimSpace(event.Data)
		if data == "" {
			return nil
		}
		if (protocol == "openai" || protocol == "responses") && data == "[DONE]" {
			done = true
			return errPetAISSEDone
		}
		if onUsage != nil {
			if usage, ok := parsePetAIUsage(data, protocol); ok {
				onUsage(usage)
			}
		}

		var delta string
		var err error
		switch protocol {
		case "openai":
			delta, err = parseOpenAITextDelta(data)
		case "responses":
			delta, err = parsePetAIResponsesTextDelta(event.Event, data, &done)
		case "anthropic":
			delta, err = parseAnthropicTextDelta(event.Event, data, &done)
		default:
			return newPetAIError(PET_AI_RESPONSE_INVALID, 0, nil)
		}
		if err != nil {
			return err
		}
		if delta == "" {
			return nil
		}
		text.WriteString(delta)
		if onDelta != nil {
			if err := onDelta(delta); err != nil {
				return err
			}
		}
		return nil
	}

	err := scanPetAISSE(body, options.MaxResponseBytes, options.MaxSSELineBytes, options.MaxSSEEventBytes, consume)
	if err != nil && !errors.Is(err, errPetAISSEDone) {
		return "", err
	}
	result := strings.TrimSpace(text.String())
	if result == "" {
		return "", newPetAIError(PET_AI_RESPONSE_INVALID, 0, nil)
	}
	_ = done
	return result, nil
}

func scanPetAISSE(
	body io.Reader,
	maxBytes int64,
	maxLineBytes int,
	maxEventBytes int,
	consume func(petAISSEEvent) error,
) error {
	if maxBytes <= 0 || maxLineBytes <= 0 || maxEventBytes <= 0 {
		return newPetAIError(PET_AI_SSE_INVALID, 0, nil)
	}
	limited := &io.LimitedReader{R: body, N: maxBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), maxLineBytes)
	var eventName string
	var data strings.Builder
	flush := func() error {
		if data.Len() == 0 && eventName == "" {
			return nil
		}
		err := consume(petAISSEEvent{Event: eventName, Data: data.String()})
		data.Reset()
		eventName = ""
		return err
	}

	for scanner.Scan() {
		if limited.N == 0 {
			// LimitedReader 已经读到了 maxBytes+1；先判响应上限，避免把被截断的
			// JSON 误报成 SSE 语法错误，调用方才能区分“内容太大”和“协议损坏”。
			return newPetAIError(PET_AI_RESPONSE_TOO_LARGE, 0, nil)
		}
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := flush(); err != nil {
				if errors.Is(err, errPetAISSEDone) {
					return err
				}
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "data:"):
			value := strings.TrimPrefix(line, "data:")
			value = strings.TrimPrefix(value, " ")
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(value)
			if data.Len() > maxEventBytes {
				return newPetAIError(PET_AI_SSE_INVALID, 0, nil)
			}
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			if len(eventName) > maxEventBytes {
				return newPetAIError(PET_AI_SSE_INVALID, 0, nil)
			}
		case strings.HasPrefix(line, "id:"), strings.HasPrefix(line, "retry:"):
			// SSE 元字段对文本抽取没有语义，不透传也不参与凭据错误文案。
		default:
			return newPetAIError(PET_AI_SSE_INVALID, 0, nil)
		}
	}
	if err := scanner.Err(); err != nil {
		return newPetAIError(PET_AI_SSE_INVALID, 0, nil)
	}
	if limited.N == 0 {
		return newPetAIError(PET_AI_RESPONSE_TOO_LARGE, 0, nil)
	}
	if err := flush(); err != nil {
		return err
	}
	return nil
}

func parseOpenAITextDelta(data string) (string, error) {
	var payload struct {
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Delta struct {
				Content *string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return "", newPetAIError(PET_AI_SSE_INVALID, 0, nil)
	}
	if len(payload.Error) > 0 && string(payload.Error) != "null" {
		return "", newPetAIError(PET_AI_UPSTREAM_ERROR, 0, nil)
	}
	if len(payload.Choices) == 0 {
		return "", nil
	}
	if payload.Choices[0].Delta.Content == nil {
		return "", nil
	}
	return *payload.Choices[0].Delta.Content, nil
}

func parsePetAIResponsesTextDelta(eventName, data string, done *bool) (string, error) {
	var payload struct {
		Type  string          `json:"type"`
		Delta string          `json:"delta"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return "", newPetAIError(PET_AI_SSE_INVALID, 0, nil)
	}
	typeName := strings.TrimSpace(payload.Type)
	if typeName == "" {
		typeName = strings.TrimSpace(eventName)
	}
	if typeName == "response.failed" || typeName == "error" || petAIResponseHasError(payload.Error) {
		return "", newPetAIError(PET_AI_UPSTREAM_ERROR, 0, nil)
	}
	if typeName == "response.incomplete" {
		return "", newPetAIError(PET_AI_RESPONSE_INVALID, 0, nil)
	}
	if typeName == "response.completed" {
		if done != nil {
			*done = true
		}
		return "", nil
	}
	if typeName != "response.output_text.delta" {
		return "", nil
	}
	return payload.Delta, nil
}

func parseAnthropicTextDelta(eventName, data string, done *bool) (string, error) {
	var payload struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return "", newPetAIError(PET_AI_SSE_INVALID, 0, nil)
	}
	if payload.Type == "" {
		payload.Type = strings.TrimSpace(eventName)
	}
	if payload.Type == "error" || (len(payload.Error) > 0 && string(payload.Error) != "null") {
		return "", newPetAIError(PET_AI_UPSTREAM_ERROR, 0, nil)
	}
	if payload.Type == "message_stop" {
		*done = true
		return "", nil
	}
	if payload.Type != "content_block_delta" && payload.Type != "text_delta" {
		return "", nil
	}
	if payload.Delta.Type != "" && payload.Delta.Type != "text_delta" {
		return "", nil
	}
	return payload.Delta.Text, nil
}

func readLimitedBody(body io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, newPetAIError(PET_AI_RESPONSE_TOO_LARGE, 0, nil)
	}
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, newPetAIError(PET_AI_UPSTREAM_ERROR, 0, nil)
	}
	if int64(len(data)) > maxBytes {
		return nil, newPetAIError(PET_AI_RESPONSE_TOO_LARGE, 0, nil)
	}
	return data, nil
}

func (s *PetAIService) upstreamStatusError(response *http.Response) error {
	// 只读取并丢弃有限正文，既避免连接未消费，也不把供应商错误正文带到错误结构。
	if response.Body != nil {
		if err := func() error {
			_, err := readLimitedBody(response.Body, s.options.MaxResponseBytes)
			return err
		}(); err != nil {
			return err
		}
	}
	return &PetAIError{Code: PET_AI_UPSTREAM_ERROR, Status: response.StatusCode}
}

func (s *PetAIService) reserveRequest(requestID string, state *petAIRequestState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.active[requestID]; exists {
		return newPetAIError(PET_AI_REQUEST_IN_FLIGHT, 0, nil)
	}
	s.active[requestID] = state
	return nil
}

func (s *PetAIService) releaseRequest(requestID string, state *petAIRequestState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.active[requestID]; ok && current == state {
		delete(s.active, requestID)
	}
}

func (s *PetAIService) emit(state *petAIRequestState, event PetAIEvent) error {
	if s == nil || s.emitter == nil {
		return newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	s.mu.Lock()
	state.sequence++
	event.Sequence = state.sequence
	s.mu.Unlock()
	if err := s.emitter.Emit(event); err != nil {
		return newPetAIError(PET_AI_EVENT_ERROR, 0, nil)
	}
	return nil
}

func classifyPetAIContextError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return newPetAIError(PET_AI_REQUEST_CANCELLED, 0, err)
	case errors.Is(err, context.DeadlineExceeded):
		return newPetAIError(PET_AI_TIMEOUT, 0, err)
	default:
		return newPetAIError(PET_AI_UPSTREAM_ERROR, 0, nil)
	}
}

func classifyPetAIExecutionError(err error, ctx context.Context) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return classifyPetAIContextError(ctx.Err())
	}
	if isPetAIError(err) || PetProviderErrorCodeOf(err) != "" {
		return err
	}
	return newPetAIError(PET_AI_UPSTREAM_ERROR, 0, nil)
}

func publicPetAIEventError(err error, ctx context.Context) *PetAIEventError {
	if err == nil {
		return nil
	}
	code := PetAIErrorCodeOf(err)
	status := 0
	var aiErr *PetAIError
	if errors.As(err, &aiErr) && aiErr != nil {
		status = aiErr.Status
	}
	if code == "" {
		if ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = string(PET_AI_TIMEOUT)
		} else {
			code = string(PET_AI_UPSTREAM_ERROR)
		}
	}
	if errors.Is(err, context.Canceled) || (ctx != nil && errors.Is(ctx.Err(), context.Canceled) && code == string(PET_AI_UPSTREAM_ERROR)) {
		code = string(PET_AI_REQUEST_CANCELLED)
	}
	retryable := code == string(PET_AI_UPSTREAM_ERROR) || code == string(PET_AI_TIMEOUT)
	if status >= http.StatusInternalServerError || status == http.StatusTooManyRequests {
		retryable = true
	}
	return &PetAIEventError{Code: code, Status: status, Retryable: retryable}
}

func isPetAIEventError(err error) bool {
	var aiErr *PetAIError
	return errors.As(err, &aiErr) && aiErr != nil && aiErr.Code == PET_AI_EVENT_ERROR
}

func isPetAIError(err error) bool {
	var aiErr *PetAIError
	return errors.As(err, &aiErr) && aiErr != nil
}

func newPetAIError(code PetAIErrorCode, status int, cause error) *PetAIError {
	return &PetAIError{Code: code, Status: status, cause: cause}
}

func runeLen(value string) int {
	return len([]rune(value))
}

func hasLineBreak(value string) bool {
	return strings.ContainsAny(value, "\r\n")
}
