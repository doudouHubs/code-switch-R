package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

const (
	AgentConversationSourceManager    = "manager"
	AgentConversationSourceChannel    = "channel"
	AgentConversationDefaultQueueSize = 32
)

// AgentConversationRequest 是所有 Agent 入口共用的内部请求契约。
// PetChatRequest 仍然保留 Wails 兼容字段；Hub 使用这里的元数据把管家和频道
// 统一投递到项目队列，同时不把频道身份混进 Codex 的用户提示。
type AgentConversationRequest struct {
	ProjectID         string
	ProjectName       string
	RequestID         string
	Source            string
	PetID             string
	SessionName       string
	ChannelInstanceID string
	ChannelChatID     string
	Persona           string
	RuntimeContext    string
	UserText          string
	Images            []PetAIImage
	Skills            []AgentSkillReference
	// LocalImages 由频道媒体落盘后注入，Hub 只负责透传，不拥有文件系统权限。
	LocalImages        []PetAILocalImage
	ToolScope          string
	ToolExecutionScope string
}

// AgentDeliveryResult 是一次统一回复向频道目标投递的结果。单个目标失败只
// 记录在该结果中，不会反向改变 Codex turn 的完成状态。
type AgentDeliveryResult struct {
	ProjectID  string `json:"projectId"`
	InstanceID string `json:"instanceId"`
	ChatID     string `json:"chatId"`
	MessageID  string `json:"messageId,omitempty"`
	Error      string `json:"error,omitempty"`
}

// AgentChannelBroadcaster 是 services 与 channels 之间的单向窄接口。它只接收
// 已完成的项目 Agent 回复，不暴露 Store、Provider 或 Codex runtime。
type AgentChannelBroadcaster interface {
	BroadcastProject(context.Context, string, string, string) []AgentDeliveryResult
}

// AgentChannelBroadcasterFunc 允许主进程用闭包接入 channels，避免 services
// 反向 import channels 形成循环依赖。
type AgentChannelBroadcasterFunc func(context.Context, string, string, string) []AgentDeliveryResult

func (f AgentChannelBroadcasterFunc) BroadcastProject(ctx context.Context, projectID, text, requestID string) []AgentDeliveryResult {
	if f == nil {
		return nil
	}
	return f(ctx, projectID, text, requestID)
}

// AgentConversationPersonaResolver 是 Agent 人格的唯一读取边界。入口可以携带
// 兼容 persona，但一旦宿主注入 resolver，Hub 必须以持久化宠物设置为准，避免
// 管家页面和频道配置各自拼出不同 fingerprint，从而把同一项目拆成两条 thread。
type AgentConversationPersonaResolver interface {
	Resolve(context.Context, string, string) (string, error)
}

// AgentConversationPersonaResolverFunc 让 main.go 可以用 PetDAO 的窄读取闭包
// 接入 canonical persona，而不把 DAO 依赖泄漏到 Hub 或 channels 包。
type AgentConversationPersonaResolverFunc func(context.Context, string, string) (string, error)

func (f AgentConversationPersonaResolverFunc) Resolve(ctx context.Context, projectID, petID string) (string, error) {
	if f == nil {
		return "", nil
	}
	return f(ctx, projectID, petID)
}

type AgentConversationHubOptions struct {
	MaxQueued       int
	Emitter         PetAIEventEmitter
	Broadcaster     AgentChannelBroadcaster
	PersonaResolver AgentConversationPersonaResolver
}

type AgentConversationHub struct {
	runtime     PetChatRuntime
	history     PetChatHistoryRuntime
	emitter     PetAIEventEmitter
	broadcaster AgentChannelBroadcaster
	persona     AgentConversationPersonaResolver
	maxQueued   int

	mu       sync.Mutex
	closed   bool
	projects map[string]*agentConversationProjectQueue
	requests map[string]*agentConversationJob
}

type agentConversationProjectQueue struct {
	running *agentConversationJob
	waiting []*agentConversationJob
}

type agentConversationJob struct {
	request    AgentConversationRequest
	command    *AgentCommandRequest
	ctx        context.Context
	cancel     context.CancelFunc
	projectKey string
	sequence   int64
	terminal   bool
}

// NewAgentConversationHub 创建项目级会话 owner。底层 runtime 仍只负责 Codex
// app-server 协议；同一项目的串行规则、事件元数据和广播在这里统一收口。
func NewAgentConversationHub(runtime PetChatRuntime, options ...AgentConversationHubOptions) *AgentConversationHub {
	var configured AgentConversationHubOptions
	if len(options) > 0 {
		configured = options[0]
	}
	maxQueued := configured.MaxQueued
	if maxQueued <= 0 {
		maxQueued = AgentConversationDefaultQueueSize
	}
	hub := &AgentConversationHub{
		runtime:     runtime,
		emitter:     configured.Emitter,
		broadcaster: configured.Broadcaster,
		persona:     configured.PersonaResolver,
		maxQueued:   maxQueued,
		projects:    make(map[string]*agentConversationProjectQueue),
		requests:    make(map[string]*agentConversationJob),
	}
	if history, ok := runtime.(PetChatHistoryRuntime); ok {
		hub.history = history
	}
	return hub
}

var _ PetChatRuntime = (*AgentConversationHub)(nil)
var _ PetChatHistoryRuntime = (*AgentConversationHub)(nil)
var _ PetCodexCommandRuntime = (*AgentConversationHub)(nil)

func (h *AgentConversationHub) StartChat(ctx context.Context, request PetChatRequest) (PetChatStartResult, error) {
	return h.StartConversation(ctx, AgentConversationRequest{
		ProjectID:          request.ProjectID,
		ProjectName:        request.ProjectName,
		RequestID:          request.RequestID,
		Source:             request.Source,
		PetID:              request.PetID,
		SessionName:        request.SessionName,
		ChannelInstanceID:  request.ChannelInstanceID,
		ChannelChatID:      request.ChannelChatID,
		Persona:            request.Persona,
		RuntimeContext:     request.RuntimeContext,
		UserText:           request.UserText,
		Images:             request.Images,
		Skills:             request.Skills,
		LocalImages:        request.LocalImages,
		ToolScope:          request.ToolScope,
		ToolExecutionScope: request.ToolExecutionScope,
	})
}

// StartConversation 只确认请求已进入项目队列，不能等待 Codex 进程启动或
// thread/resume RPC；这样 Wails 可以先拿到 requestId，前端 watchdog 不会把启动期
// 的慢 RPC误判成回复超时。
func (h *AgentConversationHub) StartConversation(ctx context.Context, request AgentConversationRequest) (PetChatStartResult, error) {
	job, queued, err := h.enqueueConversationJob(ctx, request)
	if err != nil {
		return PetChatStartResult{}, err
	}
	if queued {
		h.emitJobEvent(job, PetAIEvent{Type: PetAIEventQueued})
		return PetChatStartResult{RequestID: job.request.RequestID, Queued: true}, nil
	}
	h.startJob(job)
	return PetChatStartResult{RequestID: job.request.RequestID}, nil
}

// enqueueConversationJob 是普通聊天和会产生 turn 的控制命令共用的项目 FIFO
// 注册入口。先登记 Hub 所有权，再让 runtime 发出 started，才能保证快速同步
// 事件不会在 h.requests 中查不到 owner 后被丢弃。
func (h *AgentConversationHub) enqueueConversationJob(ctx context.Context, request AgentConversationRequest) (*agentConversationJob, bool, error) {
	if h == nil {
		return nil, false, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	normalized, err := normalizeAgentConversationRequest(request)
	if err != nil {
		return nil, false, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	jobCtx, cancel := context.WithCancel(ctx)
	job := &agentConversationJob{
		request:    normalized,
		ctx:        jobCtx,
		cancel:     cancel,
		projectKey: AgentProjectConversationKey(normalized.ProjectID, normalized.PetID),
	}

	h.mu.Lock()
	if h.closed || h.runtime == nil {
		h.mu.Unlock()
		cancel()
		return nil, false, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	if _, exists := h.requests[normalized.RequestID]; exists {
		h.mu.Unlock()
		cancel()
		return nil, false, newPetAIError(PET_AI_REQUEST_IN_FLIGHT, 0, nil)
	}
	queue := h.projects[job.projectKey]
	if queue == nil {
		queue = &agentConversationProjectQueue{}
		h.projects[job.projectKey] = queue
	}
	queued := queue.running != nil || len(queue.waiting) > 0
	if queued && len(queue.waiting) >= h.maxQueued {
		h.mu.Unlock()
		cancel()
		return nil, false, newPetAIError(PET_AI_QUEUE_FULL, 0, nil)
	}
	h.requests[normalized.RequestID] = job
	if queued {
		queue.waiting = append(queue.waiting, job)
		h.mu.Unlock()
		return job, true, nil
	}
	queue.running = job
	h.mu.Unlock()
	return job, false, nil
}

// Emit 接收底层 Codex runtime 的唯一事件出口。事件先补齐项目/来源元数据，
// 再推进队列；终态只处理一次，防止 app-server 的重复通知或迟到响应重复广播。
func (h *AgentConversationHub) Emit(event PetAIEvent) error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	// Close 后底层 app-server 可能仍把 stdout 中的迟到通知推上来；这些通知
	// 已经失去当前项目队列的所有权，必须静默丢弃，不能重新触发下一轮广播。
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	job := h.requests[event.RequestID]
	if job == nil || job.terminal {
		h.mu.Unlock()
		return nil
	}
	job.sequence++
	event = enrichAgentConversationEvent(event, job)
	event.Sequence = job.sequence
	terminal := isPetAIEventTerminal(event.Type)
	if terminal {
		job.terminal = true
		delete(h.requests, job.request.RequestID)
		if queue := h.projects[job.projectKey]; queue != nil && queue.running == job {
			queue.running = nil
		}
	}
	h.mu.Unlock()

	err := h.emit(event)
	if terminal {
		job.cancel()
		if event.Type == PetAIEventCompleted && strings.TrimSpace(event.Text) != "" && h.broadcaster != nil {
			// 频道网络可能慢或不可用；广播独立于 Codex turn 和下一条项目队列，
			// 单目标失败由 broadcaster 隔离，不让完成事件卡在平台 API 上。
			go h.broadcastCompleted(job, event.Text)
		}
		h.startNext(job.projectKey)
	}
	return err
}

func (h *AgentConversationHub) emitJobEvent(job *agentConversationJob, event PetAIEvent) {
	if job == nil {
		return
	}
	h.mu.Lock()
	if job.terminal {
		h.mu.Unlock()
		return
	}
	job.sequence++
	event = enrichAgentConversationEvent(event, job)
	event.Sequence = job.sequence
	h.mu.Unlock()
	_ = h.emit(event)
}

// finishJobEvent 是 Hub 内部唯一的“任务终态落盘”入口。
// 先前排队取消和关闭路径会先写 terminal，再复用 emitJobEvent，结果被
// emitJobEvent 当成重复终态直接跳过；这里把所有权转移、序号生成和事件发射
// 分成明确的两步，确保每个已接受请求至少有一条可观察终态。
func (h *AgentConversationHub) finishJobEvent(job *agentConversationJob, event PetAIEvent) {
	if job == nil || !isPetAIEventTerminal(event.Type) {
		return
	}
	h.mu.Lock()
	if job.terminal {
		h.mu.Unlock()
		return
	}
	job.terminal = true
	delete(h.requests, job.request.RequestID)
	if queue := h.projects[job.projectKey]; queue != nil && queue.running == job {
		queue.running = nil
	}
	job.sequence++
	event = enrichAgentConversationEvent(event, job)
	event.Sequence = job.sequence
	h.mu.Unlock()

	_ = h.emit(event)
	job.cancel()
	// 只有运行中的任务结束后才需要推进队列；排队任务被取消时调用它也
	// 是安全的，因为 running 非空时不会抢占当前 turn。
	h.startNext(job.projectKey)
}

func (h *AgentConversationHub) startJob(job *agentConversationJob) {
	if job != nil && job.command != nil {
		h.startCommandJob(job)
		return
	}
	go func() {
		writeAgentConversationStage("agent-conversation-start", job, nil)
		request := job.request
		writeAgentConversationStage("agent-conversation-persona-start", job, nil)
		persona, err := h.resolvePersona(job.ctx, request.ProjectID, request.PetID, request.Persona)
		if err != nil {
			writeAgentConversationStage("agent-conversation-persona-failed", job, err)
			h.finishStartError(job, err)
			return
		}
		// resolver 属于可注入边界，不能假定每个实现都会及时响应取消；
		// 关闭或调用方取消后禁止再把这条请求送进底层 Codex。
		if err := job.ctx.Err(); err != nil {
			writeAgentConversationStage("agent-conversation-cancelled", job, err)
			h.finishStartError(job, err)
			return
		}
		request.Persona = persona
		writeAgentConversationStage("agent-conversation-persona-ready", job, nil)
		writeAgentConversationStage("agent-conversation-runtime-start", job, nil)
		_, err = h.runtime.StartChat(job.ctx, petChatRequestFromConversation(request))
		if err != nil {
			writeAgentConversationStage("agent-conversation-runtime-failed", job, err)
			h.finishStartError(job, err)
			return
		}
		writeAgentConversationStage("agent-conversation-runtime-accepted", job, nil)
	}()
}

func (h *AgentConversationHub) startCommandJob(job *agentConversationJob) {
	go func() {
		if job == nil || job.command == nil {
			return
		}
		writeAgentConversationStage("agent-conversation-command-start", job, nil)
		if err := job.ctx.Err(); err != nil {
			writeAgentConversationStage("agent-conversation-command-cancelled", job, err)
			h.finishStartError(job, err)
			return
		}
		runtime := h.commandRuntime()
		if runtime == nil {
			err := newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
			writeAgentConversationStage("agent-conversation-command-unavailable", job, err)
			h.finishStartError(job, err)
			return
		}
		_, err := runtime.ExecuteCommand(job.ctx, *job.command)
		if err != nil {
			writeAgentConversationStage("agent-conversation-command-failed", job, err)
			h.finishStartError(job, err)
			return
		}
		writeAgentConversationStage("agent-conversation-command-accepted", job, nil)
	}()
}

func (h *AgentConversationHub) finishStartError(job *agentConversationJob, err error) {
	if job == nil {
		return
	}
	eventType := PetAIEventFailed
	if errors.Is(err, context.Canceled) || errors.Is(job.ctx.Err(), context.Canceled) {
		eventType = PetAIEventCancelled
	}
	h.finishJobEvent(job, PetAIEvent{
		Type:  eventType,
		Error: publicPetAIEventError(err, job.ctx),
	})
}

func (h *AgentConversationHub) startNext(projectKey string) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	queue := h.projects[projectKey]
	if queue == nil || queue.running != nil || len(queue.waiting) == 0 {
		h.mu.Unlock()
		return
	}
	job := queue.waiting[0]
	queue.waiting = queue.waiting[1:]
	if err := job.ctx.Err(); err != nil {
		h.mu.Unlock()
		h.finishJobEvent(job, PetAIEvent{
			Type:  PetAIEventCancelled,
			Error: publicPetAIEventError(err, job.ctx),
		})
		return
	}
	queue.running = job
	h.mu.Unlock()
	h.startJob(job)
}

func (h *AgentConversationHub) CancelChat(requestID string) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || runeLen(requestID) > PetAIMaxRequestIDLength || hasLineBreak(requestID) {
		return newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if h == nil {
		return nil
	}
	h.mu.Lock()
	job := h.requests[requestID]
	if job == nil {
		h.mu.Unlock()
		return nil
	}
	queue := h.projects[job.projectKey]
	if queue != nil && queue.running != job {
		for index, waiting := range queue.waiting {
			if waiting != job {
				continue
			}
			queue.waiting = append(queue.waiting[:index], queue.waiting[index+1:]...)
			h.mu.Unlock()
			job.cancel()
			h.finishJobEvent(job, PetAIEvent{Type: PetAIEventCancelled})
			return nil
		}
	}
	h.mu.Unlock()
	return h.runtime.CancelChat(requestID)
}

func (h *AgentConversationHub) GetChatHistory(ctx context.Context, request PetChatHistoryRequest) (PetChatHistoryResult, error) {
	if h == nil || h.history == nil {
		return PetChatHistoryResult{}, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := normalizePetChatHistoryRequest(request)
	if err != nil {
		return PetChatHistoryResult{}, err
	}
	normalized.Persona, err = h.resolvePersona(ctx, normalized.ProjectID, normalized.PetID, normalized.Persona)
	if err != nil {
		return PetChatHistoryResult{}, err
	}
	return h.history.GetChatHistory(ctx, normalized)
}

// ListSkills、ListModels 和 ExecuteCommand 仍通过 Hub 进入项目级 runtime；
// 这样 Agent 管家和频道不会因为各自调用控制命令而创建第二个 Codex owner。
func (h *AgentConversationHub) ListSkills(ctx context.Context, request AgentCommandRequest) (AgentSkillListResult, error) {
	ctx = normalizeAgentConversationContext(ctx)
	request.Command = "skills"
	prepared, runtime, err := h.prepareCommandRequest(ctx, request)
	if err != nil {
		return AgentSkillListResult{}, err
	}
	return runtime.ListSkills(ctx, prepared)
}

func (h *AgentConversationHub) ListModels(ctx context.Context, request AgentCommandRequest) (AgentModelListResult, error) {
	ctx = normalizeAgentConversationContext(ctx)
	request.Command = "models"
	prepared, runtime, err := h.prepareCommandRequest(ctx, request)
	if err != nil {
		return AgentModelListResult{}, err
	}
	return runtime.ListModels(ctx, prepared)
}

func (h *AgentConversationHub) ExecuteCommand(ctx context.Context, request AgentCommandRequest) (AgentCommandResult, error) {
	ctx = normalizeAgentConversationContext(ctx)
	prepared, runtime, err := h.prepareCommandRequest(ctx, request)
	if err != nil {
		return AgentCommandResult{}, err
	}
	if prepared.Command == "review" {
		if prepared.RequestID == "" {
			prepared.RequestID = newPetCodexCommandRequestID()
		}
		job, queued, err := h.enqueueConversationJob(ctx, AgentConversationRequest{
			ProjectID: prepared.ProjectID,
			ProjectName: prepared.ProjectName,
			RequestID: prepared.RequestID,
			Source:    firstNonEmptyAgentConversationString(prepared.Source, AgentConversationSourceManager),
			PetID:     prepared.PetID,
			SessionName: prepared.SessionName,
			Persona:   prepared.Persona,
		})
		if err != nil {
			return AgentCommandResult{}, err
		}
		// command 指针只在当前 job 启动前写入；queued 和 running 两种路径都
		// 已经登记了 Hub owner，因此 runtime 的同步 started 不会丢失。
		command := prepared
		job.command = &command
		if queued {
			h.emitJobEvent(job, PetAIEvent{Type: PetAIEventQueued})
		} else {
			h.startJob(job)
		}
		return AgentCommandResult{
			Command:   prepared.Command,
			Accepted:  true,
			RequestID: prepared.RequestID,
		}, nil
	}
	return runtime.ExecuteCommand(ctx, prepared)
}

func (h *AgentConversationHub) ResolveInteraction(ctx context.Context, request ResolveInteractionRequest) error {
	ctx = normalizeAgentConversationContext(ctx)
	runtime := h.commandRuntime()
	if runtime == nil {
		return newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	return runtime.ResolveInteraction(ctx, request)
}

func (h *AgentConversationHub) prepareCommandRequest(ctx context.Context, request AgentCommandRequest) (AgentCommandRequest, PetCodexCommandRuntime, error) {
	if h == nil {
		return AgentCommandRequest{}, nil, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	ctx = normalizeAgentConversationContext(ctx)
	prepared, err := normalizeAgentCommandRequest(request)
	if err != nil {
		return AgentCommandRequest{}, nil, err
	}
	persona, err := h.resolvePersona(ctx, prepared.ProjectID, prepared.PetID, prepared.Persona)
	if err != nil {
		return AgentCommandRequest{}, nil, err
	}
	prepared.Persona = persona
	runtime := h.commandRuntime()
	if runtime == nil {
		return AgentCommandRequest{}, nil, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	return prepared, runtime, nil
}

func normalizeAgentConversationContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (h *AgentConversationHub) commandRuntime() PetCodexCommandRuntime {
	if h == nil || h.runtime == nil {
		return nil
	}
	runtime, _ := h.runtime.(PetCodexCommandRuntime)
	return runtime
}

func (h *AgentConversationHub) resolvePersona(ctx context.Context, projectID, petID, candidate string) (string, error) {
	candidate = strings.TrimSpace(candidate)
	if h == nil || h.persona == nil {
		return candidate, nil
	}
	persona, err := h.persona.Resolve(ctx, strings.TrimSpace(projectID), strings.TrimSpace(petID))
	if err != nil {
		return "", newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, err)
	}
	persona = strings.TrimSpace(persona)
	if persona == "" {
		return "", newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, errors.New("canonical Agent persona is empty"))
	}
	return persona, nil
}

func (h *AgentConversationHub) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	terminalJobs := make([]*agentConversationJob, 0)
	for _, queue := range h.projects {
		for _, job := range queue.waiting {
			job.terminal = true
			delete(h.requests, job.request.RequestID)
			terminalJobs = append(terminalJobs, job)
		}
		queue.waiting = nil
		if queue.running != nil {
			job := queue.running
			job.terminal = true
			delete(h.requests, job.request.RequestID)
			terminalJobs = append(terminalJobs, job)
			queue.running = nil
		}
	}
	runtime := h.runtime
	h.mu.Unlock()
	for _, job := range terminalJobs {
		job.cancel()
		_ = h.emitEventWithoutOwnership(job, PetAIEvent{Type: PetAIEventCancelled})
	}
	if runtime == nil {
		return nil
	}
	return runtime.Close()
}

func (h *AgentConversationHub) emitEventWithoutOwnership(job *agentConversationJob, event PetAIEvent) error {
	if job == nil {
		return nil
	}
	h.mu.Lock()
	job.sequence++
	event = enrichAgentConversationEvent(event, job)
	event.Sequence = job.sequence
	h.mu.Unlock()
	return h.emit(event)
}

func (h *AgentConversationHub) broadcastCompleted(job *agentConversationJob, text string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := h.broadcaster.BroadcastProject(ctx, job.request.ProjectID, text, job.request.RequestID)
	for _, result := range results {
		if result.Error != "" {
			writeAgentConversationDiagnostic("agent-channel-broadcast-error", job, result)
		}
	}
}

func (h *AgentConversationHub) emit(event PetAIEvent) error {
	if h == nil || h.emitter == nil {
		return nil
	}
	return h.emitter.Emit(event)
}

func normalizeAgentConversationRequest(request AgentConversationRequest) (AgentConversationRequest, error) {
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.ProjectName = strings.TrimSpace(request.ProjectName)
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.Source = strings.ToLower(strings.TrimSpace(request.Source))
	request.PetID = strings.TrimSpace(request.PetID)
	request.SessionName = strings.TrimSpace(request.SessionName)
	request.ChannelInstanceID = strings.TrimSpace(request.ChannelInstanceID)
	request.ChannelChatID = strings.TrimSpace(request.ChannelChatID)
	request.Persona = strings.TrimSpace(request.Persona)
	request.RuntimeContext = strings.TrimSpace(request.RuntimeContext)
	request.UserText = strings.TrimSpace(request.UserText)
	request.ToolScope = strings.TrimSpace(request.ToolScope)
	request.ToolExecutionScope = strings.TrimSpace(request.ToolExecutionScope)
	if request.Source == "" {
		request.Source = AgentConversationSourceManager
	}
	if request.Source != AgentConversationSourceManager && request.Source != AgentConversationSourceChannel {
		return AgentConversationRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if request.RequestID == "" || runeLen(request.RequestID) > PetAIMaxRequestIDLength || hasLineBreak(request.RequestID) {
		return AgentConversationRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if request.PetID == "" || runeLen(request.PetID) > PetAIMaxPetIDLength || hasLineBreak(request.PetID) {
		return AgentConversationRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if request.ProjectID != "" && (runeLen(request.ProjectID) > PetAIMaxProjectFolderLength || hasLineBreak(request.ProjectID)) {
		return AgentConversationRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if runeLen(request.ProjectName) > PetAIMaxRequestIDLength || runeLen(request.SessionName) > PetAIMaxRequestIDLength || hasLineBreak(request.ProjectName) || hasLineBreak(request.SessionName) {
		return AgentConversationRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if request.Source == AgentConversationSourceChannel && (request.ProjectID == "" || request.ChannelInstanceID == "" || request.ChannelChatID == "") {
		return AgentConversationRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	return request, nil
}

func petChatRequestFromConversation(request AgentConversationRequest) PetChatRequest {
	return PetChatRequest{
		PetID:              request.PetID,
		ProjectID:          request.ProjectID,
		ProjectName:        request.ProjectName,
		RequestID:          request.RequestID,
		SessionName:        request.SessionName,
		Persona:            request.Persona,
		RuntimeContext:     request.RuntimeContext,
		UserText:           request.UserText,
		Images:             request.Images,
		Skills:             request.Skills,
		LocalImages:        request.LocalImages,
		ToolScope:          request.ToolScope,
		ToolExecutionScope: request.ToolExecutionScope,
		Source:             request.Source,
		ChannelInstanceID:  request.ChannelInstanceID,
		ChannelChatID:      request.ChannelChatID,
	}
}

func enrichAgentConversationEvent(event PetAIEvent, job *agentConversationJob) PetAIEvent {
	event.PetID = firstNonEmptyAgentConversationString(event.PetID, job.request.PetID)
	event.RequestID = job.request.RequestID
	event.ProjectID = job.request.ProjectID
	event.Source = job.request.Source
	event.ChannelInstanceID = job.request.ChannelInstanceID
	event.ChannelChatID = job.request.ChannelChatID
	return event
}

func AgentProjectConversationKey(projectID, petID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		return "project:" + projectID
	}
	return strings.TrimSpace(petID)
}

func isPetAIEventTerminal(eventType PetAIEventType) bool {
	return eventType == PetAIEventCompleted || eventType == PetAIEventFailed || eventType == PetAIEventCancelled
}

func firstNonEmptyAgentConversationString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func writeAgentConversationDiagnostic(event string, job *agentConversationJob, result AgentDeliveryResult) {
	if job == nil {
		return
	}
	WriteRuntimeDiagnosticAsync(
		event,
		fmt.Sprintf("project_id=%q", job.request.ProjectID),
		fmt.Sprintf("request_id=%q", job.request.RequestID),
		fmt.Sprintf("instance_id=%q", result.InstanceID),
		fmt.Sprintf("chat_id=%q", result.ChatID),
	)
}

func writeAgentConversationStage(event string, job *agentConversationJob, err error) {
	if job == nil {
		return
	}
	details := []string{
		fmt.Sprintf("project_id=%q", job.request.ProjectID),
		fmt.Sprintf("request_id=%q", job.request.RequestID),
		fmt.Sprintf("source=%q", job.request.Source),
	}
	if publicErr := publicPetAIEventError(err, job.ctx); publicErr != nil {
		details = append(details, fmt.Sprintf("error_code=%q", publicErr.Code))
	}
	WriteRuntimeDiagnosticAsync(event, details...)
}
