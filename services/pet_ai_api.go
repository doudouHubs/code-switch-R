package services

import "context"

// PetAIAPIService 是 Wails 与 PetAIService 之间的薄适配层。
// bridge 只持有核心服务，不拥有 provider、网络请求或持久化职责，避免在 Wails
// 方法中复制凭据处理、HTTP/SSE 解析和请求状态管理，保证这些规则只有一个 owner。
type PetAIAPIService struct {
	service     *PetAIService
	chatRuntime PetChatRuntime
}

// PetSpeechResult 是 Wails 可直接序列化的语音结果；[]byte 会由 JSON 编码为 base64。
// 这里仅返回音频数据和媒体类型，绝不把 provider 配置或 API Key 带回前端。
type PetSpeechResult struct {
	Audio     []byte `json:"audio"`
	MediaType string `json:"mediaType"`
}

// NewPetAIAPIService 创建 PetAI 的 Wails bridge。
// provider reader、HTTP transport 和事件 emitter 仍由 PetAIService 持有并管理，
// 主控只需把构造好的核心服务注入这里即可。
func NewPetAIAPIService(service *PetAIService) *PetAIAPIService {
	return &PetAIAPIService{service: service}
}

// NewPetAIAPIServiceWithChatRuntime 把主聊天切到独立 Codex runtime；其余能力
// 仍从 service 读取，保持已有浏览器 bridge 和 Wails 方法名不变。
func NewPetAIAPIServiceWithChatRuntime(service *PetAIService, chatRuntime PetChatRuntime) *PetAIAPIService {
	return &PetAIAPIService{service: service, chatRuntime: chatRuntime}
}

// StartChat 转发异步聊天请求；Wails 方法没有可注入的请求 context，取消由
// CancelChat 根据 requestId 触发核心服务保存的取消函数。
func (api *PetAIAPIService) StartChat(request PetChatRequest) (PetChatStartResult, error) {
	if runtime := api.getChatRuntime(); runtime != nil {
		return runtime.StartChat(context.Background(), request)
	}
	service, err := api.getService()
	if err != nil {
		return PetChatStartResult{}, err
	}
	return service.StartChat(context.Background(), request)
}

// CancelChat 只转发 requestId，不在 bridge 中维护第二份活动请求表。
func (api *PetAIAPIService) CancelChat(requestID string) error {
	if runtime := api.getChatRuntime(); runtime != nil {
		return runtime.CancelChat(requestID)
	}
	service, err := api.getService()
	if err != nil {
		return err
	}
	return service.CancelChat(requestID)
}

// GetChatHistory 只把历史读取转发给支持该能力的聊天 runtime；旧适配器不实现
// 这个可选接口时返回结构化依赖错误，不在 API 层伪造一份与 Codex thread 脱节的历史。
func (api *PetAIAPIService) GetChatHistory(request PetChatHistoryRequest) (PetChatHistoryResult, error) {
	return api.getChatHistory(context.Background(), request)
}

func (api *PetAIAPIService) getChatHistory(ctx context.Context, request PetChatHistoryRequest) (PetChatHistoryResult, error) {
	if runtime := api.getChatRuntime(); runtime != nil {
		if historyRuntime, ok := runtime.(PetChatHistoryRuntime); ok {
			return historyRuntime.GetChatHistory(ctx, request)
		}
		return PetChatHistoryResult{}, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	return PetChatHistoryResult{}, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
}

// ListSkills、ListModels、ExecuteCommand 和 ResolveInteraction 共享同一个
// chatRuntime；API 层不保存 skill、model 或 pending interaction 的第二份状态。
func (api *PetAIAPIService) ListSkills(request AgentCommandRequest) (AgentSkillListResult, error) {
	return api.listSkills(context.Background(), request)
}

func (api *PetAIAPIService) listSkills(ctx context.Context, request AgentCommandRequest) (AgentSkillListResult, error) {
	runtime, err := api.getCommandRuntime()
	if err != nil {
		return AgentSkillListResult{}, err
	}
	return runtime.ListSkills(ctx, request)
}

func (api *PetAIAPIService) ListModels(request AgentCommandRequest) (AgentModelListResult, error) {
	return api.listModels(context.Background(), request)
}

func (api *PetAIAPIService) listModels(ctx context.Context, request AgentCommandRequest) (AgentModelListResult, error) {
	runtime, err := api.getCommandRuntime()
	if err != nil {
		return AgentModelListResult{}, err
	}
	return runtime.ListModels(ctx, request)
}

func (api *PetAIAPIService) ExecuteCommand(request AgentCommandRequest) (AgentCommandResult, error) {
	return api.executeCommand(context.Background(), request)
}

func (api *PetAIAPIService) executeCommand(ctx context.Context, request AgentCommandRequest) (AgentCommandResult, error) {
	runtime, err := api.getCommandRuntime()
	if err != nil {
		return AgentCommandResult{}, err
	}
	return runtime.ExecuteCommand(ctx, request)
}

func (api *PetAIAPIService) ResolveInteraction(request ResolveInteractionRequest) error {
	return api.resolveInteraction(context.Background(), request)
}

func (api *PetAIAPIService) resolveInteraction(ctx context.Context, request ResolveInteractionRequest) error {
	runtime, err := api.getCommandRuntime()
	if err != nil {
		return err
	}
	return runtime.ResolveInteraction(ctx, request)
}

// GenerateDreamText 转发同步梦境文本请求，网络和 provider 解析仍由核心服务负责。
func (api *PetAIAPIService) GenerateDreamText(request PetDreamTextRequest) (string, error) {
	service, err := api.getService()
	if err != nil {
		return "", err
	}
	return service.GenerateDreamText(context.Background(), request)
}

// SynthesizeSpeech 转发同步语音请求，并把核心服务返回的音频三元组包装成稳定结果。
func (api *PetAIAPIService) SynthesizeSpeech(request PetSpeechRequest) (PetSpeechResult, error) {
	service, err := api.getService()
	if err != nil {
		return PetSpeechResult{}, err
	}
	audio, mediaType, err := service.SynthesizeSpeech(context.Background(), request)
	if err != nil {
		return PetSpeechResult{}, err
	}
	return PetSpeechResult{Audio: audio, MediaType: mediaType}, nil
}

// StartSpeechStream 返回 requestId 后由 PetAudioEventEmitter 推送 chunk/completed 等事件；
// bridge 不复制活动请求表，取消仍统一走核心服务的 context。
func (api *PetAIAPIService) StartSpeechStream(request PetSpeechRequest) (PetSpeechStartResult, error) {
	service, err := api.getService()
	if err != nil {
		return PetSpeechStartResult{}, err
	}
	return service.StartSpeechStream(context.Background(), request)
}

func (api *PetAIAPIService) CancelSpeech(requestID string) error {
	service, err := api.getService()
	if err != nil {
		return err
	}
	return service.CancelSpeech(requestID)
}

// TranscribeAudio 是桌宠录音输入的同步桥接；音频解码、multipart 边界和 provider
// 认证仍由核心服务负责，Wails 层只暴露清洗后的文本结果。
func (api *PetAIAPIService) TranscribeAudio(request PetTranscriptionRequest) (PetTranscriptionResult, error) {
	return api.transcribeAudio(context.Background(), request)
}

// transcribeAudio 保留 request context 的内部 bridge 入口；Wails 公共方法没有
// 请求 context，但浏览器 bridge 有，不能把可取消的 HTTP 请求重新降级成 Background。
func (api *PetAIAPIService) transcribeAudio(ctx context.Context, request PetTranscriptionRequest) (PetTranscriptionResult, error) {
	service, err := api.getService()
	if err != nil {
		return PetTranscriptionResult{}, err
	}
	return service.TranscribeAudio(ctx, request)
}

func (api *PetAIAPIService) getService() (*PetAIService, error) {
	if api == nil || api.service == nil {
		return nil, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	return api.service, nil
}

func (api *PetAIAPIService) getChatRuntime() PetChatRuntime {
	if api == nil {
		return nil
	}
	return api.chatRuntime
}

func (api *PetAIAPIService) getCommandRuntime() (PetCodexCommandRuntime, error) {
	if runtime := api.getChatRuntime(); runtime != nil {
		if commandRuntime, ok := runtime.(PetCodexCommandRuntime); ok {
			return commandRuntime, nil
		}
	}
	return nil, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
}
