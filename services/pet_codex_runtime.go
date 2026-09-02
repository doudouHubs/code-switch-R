package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	modelpricing "codeswitch/resources/model-pricing"
)

const petCodexRuntimeCloseBudget = 6 * time.Second

const petCodexProjectToolScopePrefix = "project:"

// PetCodexProjectToolScope 是项目级动态工具快照的稳定 scope。频道实例的
// instance/session/chat 身份不应进入 thread 的工具指纹，否则同一项目会被
// 每个频道强行拆成多条 Codex 会话。
func PetCodexProjectToolScope(projectID string) string {
	return petCodexProjectToolScopePrefix + strings.TrimSpace(projectID)
}

// PetChatRuntime 是 Wails 主聊天的窄边界。梦境、主动搭话、TTS 和转写不实现
// 这个接口，因此它们仍然由旧 PetAIService 处理，避免把两个生命周期搅成一锅粥。
type PetChatRuntime interface {
	StartChat(context.Context, PetChatRequest) (PetChatStartResult, error)
	CancelChat(string) error
	Close() error
}

// PetChatHistoryRuntime 是可选的历史读取能力。单独拆接口是为了让旧的
// PetChatRuntime 测试桩和非 Codex 适配器仍然只实现发送/取消/关闭，不被历史
// 读取这条新职责强行绑死。
type PetChatHistoryRuntime interface {
	GetChatHistory(context.Context, PetChatHistoryRequest) (PetChatHistoryResult, error)
}

type PetCodexSessionRepository interface {
	LoadCodexSession(context.Context, string) (*PetCodexSession, error)
	SaveCodexSession(context.Context, PetCodexSession) error
}

// PetCodexDynamicToolSnapshot 是一次 thread 启动所使用的完整工具快照。
// Fingerprint 不只代表 schema，还可以包含宿主的权限状态；权限变化时必须
// 创建新 thread，避免旧 thread 继续携带已经失效的能力边界。
type PetCodexDynamicToolSnapshot struct {
	Definitions []PetAgentToolDefinition
	Fingerprint string
}

// PetCodexDynamicToolProvider 把动态工具的定义和执行器留给业务 owner。
// services 只负责 app-server 协议和 turn 生命周期，不直接依赖频道包，因而
// 桌宠和频道可以共用同一个 Codex runtime 实现而保持各自的权限边界。
type PetCodexDynamicToolProvider interface {
	Snapshot(scope string) (PetCodexDynamicToolSnapshot, error)
	NewExecutor(context.Context, string, string) (PetAgentToolRunner, error)
}

type PetCodexRuntimeDependencies struct {
	Sessions                 PetCodexSessionRepository
	AgentSessions            AgentCodexSessionRepository
	AgentModelReader         PetAgentModelReader
	WorkspaceResolver        PetWorkspaceResolver
	ProjectWorkspaceResolver ProjectWorkspaceResolver
	Emitter                  PetAIEventEmitter
	ActivityEmitter          PetActivityEmitter
	DynamicToolProvider      PetCodexDynamicToolProvider
	CodexHookSourceRegistrar CodexHookSourceRegistrar
	// LocalImageRoots 是 Codex localImage 的文件系统信任边界；路径只有在
	// 这些目录内通过实时校验后才会进入 turn/start。
	LocalImageRoots []string
	CommandFactory  CodexAppServerCommandFactory
	Executable      string
	ResponseTimeout time.Duration
}

type PetCodexRuntime struct {
	sessions                 PetCodexSessionRepository
	agentSessions            AgentCodexSessionRepository
	agentModelReader         PetAgentModelReader
	workspaceResolver        PetWorkspaceResolver
	projectWorkspaceResolver ProjectWorkspaceResolver
	emitter                  PetAIEventEmitter
	activityEmitter          PetActivityEmitter
	dynamicTools             PetCodexDynamicToolProvider
	hookSourceRegistrar      CodexHookSourceRegistrar
	localImageRoots          []string
	commandFactory           CodexAppServerCommandFactory
	executable               string
	responseTimeout          time.Duration

	mu                  sync.Mutex
	closed              bool
	states              map[string]*petCodexPetState
	requests            map[string]*petCodexActiveTurn
	interactionMu       sync.Mutex
	interactions        map[string]*petCodexPendingInteraction
	interactionSequence uint64
}

// runtime diagnostic 只记录 pet/request/thread 的标识和阶段，不记录用户输入或模型输出；
// 这样桌面端复现超时后可以区分 RPC、通知消费和 Wails 事件链，而不会把聊天内容落盘。
func writePetCodexDiagnostic(event string, details ...string) {
	fields := make([]string, 0, len(details)+1)
	fields = append(fields, "component=pet-codex-runtime")
	fields = append(fields, details...)
	// 诊断文件是旁路证据，不能让通知消费等待磁盘 I/O，尤其是 completed 前的
	// 最后一个 delta；有界队列保持异步，同时避免每条通知都创建写盘 goroutine。
	WriteRuntimeDiagnosticAsync(event, fields...)
}

type petCodexPetState struct {
	petID     string
	projectID string
	stateKey  string
	mu        sync.Mutex
	// startMu 只串行化同一只宠物的 session/turn 启动流程；通知消费不拿这把锁，
	// 否则 turn/start 等待期间 stdout reader 可能被通知队列反向堵住。
	startMu sync.Mutex

	client             *CodexAppServerClient
	threadID           string
	workspace          string
	personaFingerprint string
	protocolVersion    int
	modelProvider      string
	model              string
	toolScope          string
	toolFingerprint    string
	toolNames          map[PetAgentToolName]struct{}
	active             *petCodexActiveTurn
}

type petCodexActiveTurn struct {
	state          *petCodexPetState
	request        petCodexChatInput
	client         *CodexAppServerClient
	modelReference PetAgentModelReference
	// startCancel 覆盖 session 握手和 turn/start 的启动阶段。取消时如果还没有
	// turnId，必须先终止这条 context，避免 Wails 的同步调用已经返回后仍留下孤儿启动任务。
	startCancel context.CancelFunc
	turnID      string
	// 恢复旧 thread 时，Codex 可能在新 turn 启动窗口内补发旧 turn 的终态；
	// 这些 ID 必须显式排除，不能只依赖“当前 turnID 为空”的宽松匹配。
	staleTurnIDs  map[string]struct{}
	sequence      int64
	text          strings.Builder
	completedText string
	usage         modelpricing.UsageSnapshot
	usageSeen     bool
	toolCalls     map[string]struct{}
	activity      *petActivityRequest
	cancelled     bool
	interruptSent bool
}

type petCodexStartFailure struct {
	owned       bool
	client      *CodexAppServerClient
	threadID    string
	turnID      string
	cancelled   bool
	startCancel context.CancelFunc
}

type petCodexChatInput struct {
	PetID              string
	ProjectID          string
	ProjectName        string
	RequestID          string
	Source             string
	ChannelInstanceID  string
	ChannelChatID      string
	SessionName        string
	Persona            string
	RuntimeContext     string
	UserText           string
	Images             []PetAIImage
	Skills             []AgentSkillReference
	LocalImages        []PetAILocalImage
	ToolScope          string
	ToolExecutionScope string
	ToolFingerprint    string
	ToolDefinitions    []PetAgentToolDefinition
}

type petCodexThreadResponse struct {
	Thread struct {
		ID     string `json:"id"`
		CWD    string `json:"cwd"`
		Status any    `json:"status"`
		Turns  []struct {
			ID     string            `json:"id"`
			Status string            `json:"status"`
			Items  []json.RawMessage `json:"items"`
		} `json:"turns"`
	} `json:"thread"`
	Model                 string   `json:"model"`
	ModelProvider         string   `json:"modelProvider"`
	CWD                   string   `json:"cwd"`
	RuntimeWorkspaceRoots []string `json:"runtimeWorkspaceRoots"`
	ApprovalPolicy        string   `json:"approvalPolicy"`
	Sandbox               struct {
		Type          string   `json:"type"`
		WritableRoots []string `json:"writableRoots"`
		NetworkAccess bool     `json:"networkAccess"`
	} `json:"sandbox"`
	InitialTurnsPage *struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	} `json:"initialTurnsPage"`
}

type petCodexTurnStartedNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Turn     struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"turn"`
}

type petCodexDeltaNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Delta    string `json:"delta"`
}

type petCodexUsageNotification struct {
	ThreadID   string `json:"threadId"`
	TurnID     string `json:"turnId"`
	TokenUsage struct {
		Last struct {
			InputTokens       int64 `json:"inputTokens"`
			CachedInputTokens int64 `json:"cachedInputTokens"`
			CacheWriteTokens  int64 `json:"cacheWriteInputTokens"`
			OutputTokens      int64 `json:"outputTokens"`
			ReasoningTokens   int64 `json:"reasoningOutputTokens"`
		} `json:"last"`
	} `json:"tokenUsage"`
}

type petCodexTurnCompletedNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Turn     struct {
		ID     string            `json:"id"`
		Status string            `json:"status"`
		Items  []json.RawMessage `json:"items"`
	} `json:"turn"`
}

type petCodexItemCompletedNotification struct {
	ThreadID string          `json:"threadId"`
	TurnID   string          `json:"turnId"`
	Item     json.RawMessage `json:"item"`
}

// NewPetCodexRuntime 创建惰性宠物 Codex runtime。构造时不启动任何进程，只有
// 绑定项目且收到主聊天请求时才为对应宠物建立 app-server。
func NewPetCodexRuntime(deps PetCodexRuntimeDependencies) *PetCodexRuntime {
	responseTimeout := deps.ResponseTimeout
	if responseTimeout <= 0 {
		responseTimeout = defaultCodexAppServerResponseTimeout
	}
	return &PetCodexRuntime{
		sessions:                 deps.Sessions,
		agentSessions:            deps.AgentSessions,
		agentModelReader:         deps.AgentModelReader,
		workspaceResolver:        deps.WorkspaceResolver,
		projectWorkspaceResolver: deps.ProjectWorkspaceResolver,
		emitter:                  deps.Emitter,
		activityEmitter:          deps.ActivityEmitter,
		dynamicTools:             deps.DynamicToolProvider,
		hookSourceRegistrar:      deps.CodexHookSourceRegistrar,
		localImageRoots:          append([]string(nil), deps.LocalImageRoots...),
		commandFactory:           deps.CommandFactory,
		executable:               strings.TrimSpace(deps.Executable),
		responseTimeout:          responseTimeout,
		states:                   make(map[string]*petCodexPetState),
		requests:                 make(map[string]*petCodexActiveTurn),
		interactions:             make(map[string]*petCodexPendingInteraction),
	}
}

var _ PetChatRuntime = (*PetCodexRuntime)(nil)

func (r *PetCodexRuntime) StartChat(ctx context.Context, request PetChatRequest) (PetChatStartResult, error) {
	input, err := normalizePetCodexChatRequest(request)
	if err != nil {
		return PetChatStartResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	modelReference, err := r.loadPetAgentModelReference(ctx, input.PetID)
	if err != nil {
		return PetChatStartResult{}, err
	}
	input.LocalImages, err = r.validateLocalImages(input.LocalImages)
	if err != nil {
		return PetChatStartResult{}, err
	}
	writePetCodexDiagnostic(
		"pet-codex-start",
		fmt.Sprintf("pet_id=%q", input.PetID),
		fmt.Sprintf("project_id=%q", input.ProjectID),
		fmt.Sprintf("request_id=%q", input.RequestID),
		fmt.Sprintf("model_id=%q", modelReference.ModelID),
		fmt.Sprintf("reasoning_effort=%q", modelReference.ReasoningEffort),
	)
	if r == nil || !r.hasWorkspaceResolver(input.ProjectID) || !r.hasSessionRepository(input.ProjectID) {
		return PetChatStartResult{}, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	toolSnapshot, err := r.snapshotDynamicTools(input.ToolScope)
	if err != nil {
		return PetChatStartResult{}, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, err)
	}
	input.ToolFingerprint = toolSnapshot.Fingerprint
	input.ToolDefinitions = toolSnapshot.Definitions

	state := r.stateForConversation(input.ProjectID, input.PetID)
	state.mu.Lock()
	hasActive := state.active != nil
	state.mu.Unlock()
	if hasActive {
		return PetChatStartResult{}, newPetAIError(PET_AI_REQUEST_IN_FLIGHT, 0, nil)
	}
	startCtx, startCancel := context.WithCancel(ctx)
	active := &petCodexActiveTurn{
		state:          state,
		request:        input,
		modelReference: modelReference,
		startCancel:    startCancel,
		toolCalls:      make(map[string]struct{}),
		activity: newPetActivityRequest(
			r.activityEmitter,
			PetActivitySourcePetAI,
			input.RequestID,
			input.PetID,
		),
	}
	if !r.registerActive(active, nil) {
		startCancel()
		if r.isClosed() {
			return PetChatStartResult{}, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
		}
		return PetChatStartResult{}, newPetAIError(PET_AI_REQUEST_IN_FLIGHT, 0, nil)
	}
	if err := r.emitActiveEvent(active, PetAIEvent{Type: PetAIEventStarted}); err != nil {
		failure := r.releaseActiveForStartFailure(active)
		r.removeRequest(input.RequestID, active)
		if failure.startCancel != nil {
			failure.startCancel()
		}
		if failure.owned {
			r.finishActivity(active, PetActivityFailed)
			_ = r.emitActiveEvent(active, PetAIEvent{
				Type:  PetAIEventFailed,
				Error: &PetAIEventError{Code: string(PET_AI_EVENT_ERROR)},
			})
		}
		return PetChatStartResult{}, err
	}
	// StartChat 的公开契约是“后端已接收”，不能把 Codex 进程启动、thread/resume
	// 或旧 turn 清理的 RPC 延迟暴露给 Wails。否则前端拿不到 requestId，watchdog
	// 会先把一次仍在启动中的请求判成回复超时。
	go r.startTurn(startCtx, active)
	writePetCodexDiagnostic(
		"pet-codex-start-returned",
		fmt.Sprintf("pet_id=%q", input.PetID),
		fmt.Sprintf("request_id=%q", input.RequestID),
	)
	return PetChatStartResult{RequestID: input.RequestID}, nil
}

func (r *PetCodexRuntime) startTurn(ctx context.Context, active *petCodexActiveTurn) {
	if r == nil || active == nil || active.state == nil {
		return
	}
	state := active.state
	writePetCodexDiagnostic(
		"pet-codex-worker-start",
		fmt.Sprintf("pet_id=%q", active.request.PetID),
		fmt.Sprintf("request_id=%q", active.request.RequestID),
	)

	// 同一只宠物的 session 握手必须串行；锁只覆盖握手，不覆盖 turn/start，
	// 这样 completed 先于 turn/start 响应到达时，下一条消息仍能及时接管 client。
	state.startMu.Lock()
	workspace, err := r.resolveConversationWorkspace(ctx, active.request.ProjectID, active.request.PetID)
	if err != nil {
		state.startMu.Unlock()
		writePetCodexDiagnostic(
			"pet-codex-workspace-error",
			fmt.Sprintf("pet_id=%q", active.request.PetID),
			fmt.Sprintf("request_id=%q", active.request.RequestID),
			fmt.Sprintf("error_code=%q", PetAIErrorCodeOf(err)),
		)
		r.finishStartFailure(active, petCodexStartErrorCode(err), nil)
		return
	}
	writePetCodexDiagnostic(
		"pet-codex-workspace-ready",
		fmt.Sprintf("pet_id=%q", active.request.PetID),
		fmt.Sprintf("workspace=%q", workspace),
	)

	client, staleTurnIDs, err := r.ensureSession(
		ctx,
		state,
		workspace,
		active.request.ProjectID,
		active.request.PetID,
		active.request.Persona,
		active.modelReference,
		active.request.ToolScope,
		PetCodexDynamicToolSnapshot{
			Definitions: active.request.ToolDefinitions,
			Fingerprint: active.request.ToolFingerprint,
		},
	)
	if err != nil {
		state.startMu.Unlock()
		writePetCodexDiagnostic(
			"pet-codex-session-error",
			fmt.Sprintf("pet_id=%q", active.request.PetID),
			fmt.Sprintf("request_id=%q", active.request.RequestID),
			fmt.Sprintf("error_code=%q", PetAIErrorCodeOf(err)),
		)
		r.finishStartFailure(active, petCodexStartErrorCode(err), nil)
		return
	}

	state.mu.Lock()
	owned := state.active == active
	cancelled := active.cancelled
	if owned {
		active.client = client
		active.staleTurnIDs = make(map[string]struct{}, len(staleTurnIDs))
		for _, staleTurnID := range staleTurnIDs {
			if staleTurnID = strings.TrimSpace(staleTurnID); staleTurnID != "" {
				active.staleTurnIDs[staleTurnID] = struct{}{}
			}
		}
	}
	state.mu.Unlock()
	state.startMu.Unlock()
	if !owned {
		return
	}
	state.mu.Lock()
	threadID := strings.TrimSpace(state.threadID)
	state.mu.Unlock()
	r.registerCodexHookSource(active.request, workspace, threadID, "")
	if cancelled || ctx.Err() != nil {
		r.finishStartFailure(active, PET_AI_REQUEST_CANCELLED, nil)
		return
	}
	if err := r.validatePetCodexSkills(ctx, client, workspace, active.request.Skills); err != nil {
		code := petCodexStartErrorCode(err)
		if code == PET_AI_UPSTREAM_ERROR {
			code = PET_AI_INVALID_REQUEST
		}
		r.finishStartFailure(active, code, nil)
		return
	}

	writePetCodexDiagnostic(
		"pet-codex-turn-start",
		fmt.Sprintf("pet_id=%q", active.request.PetID),
		fmt.Sprintf("request_id=%q", active.request.RequestID),
	)
	response, err := client.Call(ctx, "turn/start", r.buildTurnStartParams(active))
	if err != nil {
		writePetCodexDiagnostic(
			"pet-codex-turn-start-error",
			fmt.Sprintf("pet_id=%q", active.request.PetID),
			fmt.Sprintf("request_id=%q", active.request.RequestID),
			fmt.Sprintf("error_code=%q", petCodexStartErrorCode(err)),
		)
		// turn/start 已经写入 app-server，响应超时后不能复用这个 client，
		// 否则迟到的旧 turn 通知可能被下一轮 active 误认成自己的输出。
		r.finishStartFailure(active, petCodexStartErrorCode(err), client)
		return
	}
	var result struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(response, &result); err != nil || strings.TrimSpace(result.Turn.ID) == "" {
		r.finishStartFailure(active, PET_AI_RESPONSE_INVALID, client)
		return
	}

	state.mu.Lock()
	owned = state.active == active
	if owned && active.turnID == "" {
		active.turnID = strings.TrimSpace(result.Turn.ID)
	}
	cancelled = owned && active.cancelled
	state.mu.Unlock()
	if !owned {
		// Codex 可能在 turn/start 响应前就完成了 turn；终态事件已经由通知
		// owner 发出，后台启动任务只需收口，不再向 Wails 返回第二个结果。
		return
	}
	r.registerCodexHookSource(active.request, workspace, threadID, active.turnID)
	writePetCodexDiagnostic(
		"pet-codex-turn-start-accepted",
		fmt.Sprintf("pet_id=%q", active.request.PetID),
		fmt.Sprintf("request_id=%q", active.request.RequestID),
		fmt.Sprintf("turn_id=%q", active.turnID),
	)
	if cancelled {
		r.interruptTurn(active)
	}
}

func (r *PetCodexRuntime) registerCodexHookSource(request petCodexChatInput, workspace, threadID, turnID string) {
	if r == nil || r.hookSourceRegistrar == nil || strings.TrimSpace(threadID) == "" {
		return
	}
	workspace = normalizeProjectManagerProjectPath(workspace)
	projectName := strings.TrimSpace(request.ProjectName)
	if projectName == "" {
		projectName = filepath.Base(workspace)
	}
	if projectName == "." || projectName == string(filepath.Separator) || projectName == "" {
		projectName = strings.TrimSpace(request.ProjectID)
	}
	sessionName := strings.TrimSpace(request.SessionName)
	if sessionName == "" {
		if request.Source == AgentConversationSourceManager {
			sessionName = "Agent 管家"
		} else {
			sessionName = "频道会话"
		}
	}
	threadID = strings.TrimSpace(threadID)
	// 当前 Codex Hook payload 的 session_id 通常就是 thread 身份；同时登记
	// ThreadID 兼容新版本显式拆出 thread_id 的 payload。
	r.hookSourceRegistrar.RegisterCodexHookSource(CodexHookSource{
		Source:            request.Source,
		ProjectID:         request.ProjectID,
		ProjectPath:       workspace,
		ProjectName:       projectName,
		SessionName:       sessionName,
		SessionID:         threadID,
		ThreadID:          threadID,
		TurnID:            strings.TrimSpace(turnID),
		ChannelInstanceID: request.ChannelInstanceID,
		ChannelChatID:     request.ChannelChatID,
	})
}

func (r *PetCodexRuntime) CancelChat(requestID string) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || runeLen(requestID) > PetAIMaxRequestIDLength || hasLineBreak(requestID) {
		return newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if r == nil {
		return nil
	}
	r.mu.Lock()
	active := r.requests[requestID]
	r.mu.Unlock()
	if active == nil {
		return nil
	}
	active.state.mu.Lock()
	if active.state.active != active {
		active.state.mu.Unlock()
		return nil
	}
	active.cancelled = true
	turnID := active.turnID
	client := active.client
	startCancel := active.startCancel
	active.state.mu.Unlock()
	if turnID != "" && client != nil {
		r.interruptTurn(active)
	} else if startCancel != nil {
		// session 尚未完成或 turn/start 尚未收到 turnId 时，没有可供 interrupt
		// 的目标；取消启动 context 才能让后台 worker 和当前 RPC 一起收口。
		startCancel()
	}
	return nil
}

func (r *PetCodexRuntime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	states := make([]*petCodexPetState, 0, len(r.states))
	for _, state := range r.states {
		states = append(states, state)
	}
	r.requests = make(map[string]*petCodexActiveTurn)
	r.mu.Unlock()
	// 先拒绝所有仍挂在 UI 上的审批/表单请求，再关闭 app-server；否则 Codex
	// 会一直等待 server-request 响应，退出路径可能被拖到超时预算末端。
	r.closePendingInteractions()

	clients := make([]*CodexAppServerClient, 0, len(states))
	for _, state := range states {
		state.mu.Lock()
		client := state.client
		startCancel := context.CancelFunc(nil)
		if state.active != nil {
			startCancel = state.active.startCancel
			state.active.startCancel = nil
		}
		state.active = nil
		state.client = nil
		state.mu.Unlock()
		if startCancel != nil {
			startCancel()
		}
		if client != nil {
			clients = append(clients, client)
		}
	}
	if len(clients) == 0 {
		return nil
	}

	writePetCodexDiagnostic(
		"pet-codex-close-start",
		fmt.Sprintf("clients=%d", len(clients)),
	)
	closeCtx, cancel := context.WithTimeout(context.Background(), petCodexRuntimeCloseBudget)
	defer cancel()
	results := make(chan error, len(clients))
	for _, client := range clients {
		go func(client *CodexAppServerClient) {
			results <- client.Close()
		}(client)
	}
	var firstErr error
	for completed := 0; completed < len(clients); completed++ {
		select {
		case err := <-results:
			if err != nil && firstErr == nil {
				firstErr = err
			}
		case <-closeCtx.Done():
			writePetCodexDiagnostic(
				"pet-codex-close-timeout",
				fmt.Sprintf("clients=%d", len(clients)),
			)
			return closeCtx.Err()
		}
	}
	if firstErr != nil {
		writePetCodexDiagnostic("pet-codex-close-error", fmt.Sprintf("error=%q", firstErr.Error()))
		return firstErr
	}
	writePetCodexDiagnostic("pet-codex-close-complete", fmt.Sprintf("clients=%d", len(clients)))
	return nil
}

func normalizePetCodexChatRequest(request PetChatRequest) (petCodexChatInput, error) {
	petID := strings.TrimSpace(request.PetID)
	projectID := strings.TrimSpace(request.ProjectID)
	requestID := strings.TrimSpace(request.RequestID)
	source := strings.ToLower(strings.TrimSpace(request.Source))
	if source == "" {
		source = AgentConversationSourceManager
	}
	persona := strings.TrimSpace(request.Persona)
	projectName := strings.TrimSpace(request.ProjectName)
	runtimeContext := strings.TrimSpace(request.RuntimeContext)
	userText := strings.TrimSpace(request.UserText)
	sessionName := strings.TrimSpace(request.SessionName)
	toolScope := strings.TrimSpace(request.ToolScope)
	if petID == "" || runeLen(petID) > PetAIMaxPetIDLength || hasLineBreak(petID) {
		return petCodexChatInput{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if runeLen(projectID) > PetAIMaxProjectFolderLength || hasLineBreak(projectID) || strings.IndexByte(projectID, 0) >= 0 {
		return petCodexChatInput{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if requestID == "" || runeLen(requestID) > PetAIMaxRequestIDLength || hasLineBreak(requestID) {
		return petCodexChatInput{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if source != AgentConversationSourceManager && source != AgentConversationSourceChannel {
		return petCodexChatInput{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	// ToolScope 只由宿主 runtime 注入，内部使用 NUL 分隔实例/会话/chat；
	// 这里不能复用 projectFolder 的路径校验，也不能把它拼进模型提示。
	if runeLen(toolScope) > PetAIMaxTotalInputLength || hasLineBreak(toolScope) {
		return petCodexChatInput{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if runeLen(persona) > PetAIMaxPersonaLength || runeLen(runtimeContext) > PetAIMaxInstructionLength {
		return petCodexChatInput{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if runeLen(projectName) > PetAIMaxRequestIDLength || runeLen(sessionName) > PetAIMaxRequestIDLength || hasLineBreak(projectName) || hasLineBreak(sessionName) {
		return petCodexChatInput{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	images, _, err := normalizePetAIImages(request.Images)
	if err != nil {
		return petCodexChatInput{}, err
	}
	localImages, err := normalizePetCodexLocalImageShape(request.LocalImages)
	if err != nil {
		return petCodexChatInput{}, err
	}
	skills, err := normalizePetCodexSkillReferences(request.Skills)
	if err != nil {
		return petCodexChatInput{}, err
	}
	if len(images)+len(localImages) > PetAIMaxImageCount {
		return petCodexChatInput{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if userText == "" && len(images) == 0 && len(localImages) == 0 || runeLen(userText) > PetAIMaxUserTextLength {
		return petCodexChatInput{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if runeLen(persona)+runeLen(runtimeContext)+runeLen(userText) > PetAIMaxTotalInputLength {
		return petCodexChatInput{}, newPetAIError(PET_AI_REQUEST_TOO_LARGE, 0, nil)
	}
	if projectID != "" && toolScope == "" {
		// 项目级请求默认使用稳定的项目工具快照；频道入口可以显式传入同一
		// scope，同时额外提供 ToolExecutionScope 做当前频道权限校验。
		toolScope = PetCodexProjectToolScope(projectID)
	}
	return petCodexChatInput{
		PetID:              petID,
		ProjectID:          projectID,
		ProjectName:        projectName,
		RequestID:          requestID,
		Source:             source,
		ChannelInstanceID:  strings.TrimSpace(request.ChannelInstanceID),
		ChannelChatID:      strings.TrimSpace(request.ChannelChatID),
		SessionName:        sessionName,
		Persona:            persona,
		RuntimeContext:     runtimeContext,
		UserText:           userText,
		Images:             images,
		Skills:             skills,
		LocalImages:        localImages,
		ToolScope:          toolScope,
		ToolExecutionScope: strings.TrimSpace(request.ToolExecutionScope),
	}, nil
}

func (r *PetCodexRuntime) snapshotDynamicTools(scope string) (PetCodexDynamicToolSnapshot, error) {
	if r == nil || r.dynamicTools == nil {
		return PetCodexDynamicToolSnapshot{}, nil
	}
	snapshot, err := r.dynamicTools.Snapshot(strings.TrimSpace(scope))
	if err != nil {
		return PetCodexDynamicToolSnapshot{}, err
	}
	return normalizePetCodexDynamicToolSnapshot(snapshot)
}

func normalizePetCodexDynamicToolSnapshot(snapshot PetCodexDynamicToolSnapshot) (PetCodexDynamicToolSnapshot, error) {
	definitions := make([]PetAgentToolDefinition, 0, len(snapshot.Definitions))
	seen := make(map[PetAgentToolName]struct{}, len(snapshot.Definitions))
	for _, definition := range snapshot.Definitions {
		definition.Name = PetAgentToolName(strings.TrimSpace(string(definition.Name)))
		if definition.Name == "" {
			return PetCodexDynamicToolSnapshot{}, errors.New("dynamic tool name is empty")
		}
		if _, exists := seen[definition.Name]; exists {
			return PetCodexDynamicToolSnapshot{}, fmt.Errorf("dynamic tool %q is duplicated", definition.Name)
		}
		seen[definition.Name] = struct{}{}
		definition.Description = strings.TrimSpace(definition.Description)
		if definition.InputSchema == nil {
			definition.InputSchema = map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			}
		}
		definitions = append(definitions, definition)
	}

	fingerprint := strings.TrimSpace(snapshot.Fingerprint)
	if fingerprint == "" {
		encoded, err := json.Marshal(definitions)
		if err != nil {
			return PetCodexDynamicToolSnapshot{}, fmt.Errorf("encode dynamic tool snapshot: %w", err)
		}
		digest := sha256.Sum256(encoded)
		fingerprint = hex.EncodeToString(digest[:])
	}
	return PetCodexDynamicToolSnapshot{Definitions: definitions, Fingerprint: fingerprint}, nil
}

func petCodexDynamicToolDefinitions(definitions []PetAgentToolDefinition) []map[string]any {
	result := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, map[string]any{
			"type":        "function",
			"name":        string(definition.Name),
			"description": definition.Description,
			"inputSchema": definition.InputSchema,
		})
	}
	return result
}

func petCodexToolNames(definitions []PetAgentToolDefinition) map[PetAgentToolName]struct{} {
	names := make(map[PetAgentToolName]struct{}, len(definitions))
	for _, definition := range definitions {
		if name := PetAgentToolName(strings.TrimSpace(string(definition.Name))); name != "" {
			names[name] = struct{}{}
		}
	}
	return names
}

func (r *PetCodexRuntime) resolveConversationWorkspace(ctx context.Context, projectID, petID string) (string, error) {
	var (
		workspace string
		err       error
	)
	if strings.TrimSpace(projectID) != "" {
		if r == nil || r.projectWorkspaceResolver == nil {
			return "", newPetAIError(PET_AI_WORKSPACE_UNAVAILABLE, 0, errors.New("project workspace resolver is unavailable"))
		}
		workspace, err = r.projectWorkspaceResolver.ResolveProject(ctx, strings.TrimSpace(projectID))
	} else {
		if r == nil || r.workspaceResolver == nil {
			return "", newPetAIError(PET_AI_WORKSPACE_UNAVAILABLE, 0, errors.New("pet workspace resolver is unavailable"))
		}
		workspace, err = r.workspaceResolver.Resolve(ctx, petID)
	}
	if err != nil {
		return "", newPetAIError(PET_AI_WORKSPACE_UNAVAILABLE, 0, err)
	}
	workspace, err = normalizePetAIProjectFolder(workspace)
	if err != nil || workspace == "" {
		return "", newPetAIError(PET_AI_WORKSPACE_UNAVAILABLE, 0, err)
	}
	// 目录必须已经存在且根目录经过同一套安全解析；Codex 本身拥有 workspace
	// 工具，但不能替 runtime 兜底一个不存在或未绑定的路径。
	executor, err := NewPetAgentToolExecutor(workspace)
	if err != nil {
		return "", newPetAIError(PET_AI_WORKSPACE_UNAVAILABLE, 0, err)
	}
	return executor.WorkspaceRoot(), nil
}

func (r *PetCodexRuntime) resolveWorkspace(ctx context.Context, petID string) (string, error) {
	return r.resolveConversationWorkspace(ctx, "", petID)
}

func (r *PetCodexRuntime) hasWorkspaceResolver(projectID string) bool {
	if strings.TrimSpace(projectID) != "" {
		return r != nil && r.projectWorkspaceResolver != nil
	}
	return r != nil && r.workspaceResolver != nil
}

func (r *PetCodexRuntime) hasSessionRepository(projectID string) bool {
	if strings.TrimSpace(projectID) != "" {
		return r != nil && r.agentSessions != nil
	}
	return r != nil && r.sessions != nil
}

func petCodexStateKey(projectID, petID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		return PetCodexProjectToolScope(projectID)
	}
	return strings.TrimSpace(petID)
}

func (r *PetCodexRuntime) stateForConversation(projectID, petID string) *petCodexPetState {
	projectID = strings.TrimSpace(projectID)
	petID = strings.TrimSpace(petID)
	stateKey := petCodexStateKey(projectID, petID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.states == nil {
		r.states = make(map[string]*petCodexPetState)
	}
	state := r.states[stateKey]
	if state == nil {
		state = &petCodexPetState{petID: petID, projectID: projectID, stateKey: stateKey}
		r.states[stateKey] = state
	}
	return state
}

func (r *PetCodexRuntime) stateForPet(petID string) *petCodexPetState {
	return r.stateForConversation("", petID)
}

func (r *PetCodexRuntime) registerActive(active *petCodexActiveTurn, client *CodexAppServerClient) bool {
	if r == nil || active == nil || active.state == nil {
		return false
	}
	// 统一采用 runtime -> state 的锁顺序。Close 只在释放 runtime 锁后再进入
	// state，避免启动注册和应用关闭互相等待。
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	active.state.mu.Lock()
	defer active.state.mu.Unlock()
	if active.state.active != nil || (client != nil && active.state.client != client) {
		return false
	}
	active.state.active = active
	r.requests[active.request.RequestID] = active
	return true
}

func (r *PetCodexRuntime) attachClient(state *petCodexPetState, client *CodexAppServerClient) bool {
	if r == nil || state == nil || client == nil {
		return false
	}
	// client 挂载与 Close 共用 runtime -> state 的锁顺序，保证关闭开始后不会
	// 再把新进程挂入状态树，避免应用退出遗漏一个后台 app-server。
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.client != nil {
		return false
	}
	state.client = client
	return true
}

func (r *PetCodexRuntime) detachClient(state *petCodexPetState, client *CodexAppServerClient) {
	if state == nil || client == nil {
		return
	}
	state.mu.Lock()
	if state.client == client {
		state.client = nil
	}
	state.mu.Unlock()
}

func petCodexStartErrorCode(err error) PetAIErrorCode {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return PET_AI_TIMEOUT
	case errors.Is(err, context.Canceled):
		return PET_AI_REQUEST_CANCELLED
	case errors.Is(err, errCodexAppServerExited):
		// 进程退出不是模型业务失败；把它归到依赖不可用，UI 才能给出
		// “Codex 已退出/登录或进程状态异常”的稳定提示，且不会把退出竞态
		// 伪装成一次可重试的普通 upstream 错误。
		return PET_AI_DEPENDENCY_UNAVAILABLE
	case PetAIErrorCodeOf(err) != "":
		return PetAIErrorCode(PetAIErrorCodeOf(err))
	default:
		return PET_AI_UPSTREAM_ERROR
	}
}

func (r *PetCodexRuntime) finishStartFailure(active *petCodexActiveTurn, code PetAIErrorCode, client *CodexAppServerClient) {
	if active == nil {
		return
	}
	failure := r.releaseActiveForStartFailure(active)
	r.removeRequest(active.request.RequestID, active)
	if failure.startCancel != nil {
		failure.startCancel()
	}
	if !failure.owned {
		return
	}
	if client != nil {
		// turn/start 已经写入 client；即使当前请求已经被取消，也不能让
		// 迟到的通知进入下一条 active，因此只在本次请求仍是 owner 时回收它。
		_ = client.Close()
	}
	_ = r.failStartedTurn(active, code, failure.cancelled)
}

type petCodexLoadedSession struct {
	session *PetCodexSession
	legacy  bool
}

func (r *PetCodexRuntime) loadSession(ctx context.Context, projectID, petID string) (petCodexLoadedSession, error) {
	projectID = strings.TrimSpace(projectID)
	petID = strings.TrimSpace(petID)
	if projectID != "" {
		if r == nil || r.agentSessions == nil {
			return petCodexLoadedSession{}, errors.New("agent Codex session repository is unavailable")
		}
		agentSession, err := r.agentSessions.LoadAgentCodexSession(ctx, projectID)
		if err != nil {
			return petCodexLoadedSession{}, err
		}
		if agentSession != nil {
			return petCodexLoadedSession{session: &PetCodexSession{
				PetID:              petID,
				ThreadID:           agentSession.ThreadID,
				Workspace:          agentSession.Workspace,
				PersonaFingerprint: agentSession.PersonaFingerprint,
				ToolFingerprint:    agentSession.ToolFingerprint,
				ProtocolVersion:    agentSession.ProtocolVersion,
				UpdatedAt:          agentSession.UpdatedAt,
			}}, nil
		}
		// 只在项目 session 尚不存在时读取旧 pet session；一旦项目表已有
		// 记录，它就是新的事实源，不能再被旧宠物数据反向覆盖。
		if r.sessions == nil {
			return petCodexLoadedSession{}, nil
		}
		legacySession, err := r.sessions.LoadCodexSession(ctx, petID)
		if err != nil {
			return petCodexLoadedSession{}, err
		}
		return petCodexLoadedSession{session: legacySession, legacy: legacySession != nil}, nil
	}
	if r == nil || r.sessions == nil {
		return petCodexLoadedSession{}, errors.New("pet Codex session repository is unavailable")
	}
	session, err := r.sessions.LoadCodexSession(ctx, petID)
	return petCodexLoadedSession{session: session}, err
}

func (r *PetCodexRuntime) saveSession(ctx context.Context, projectID, petID string, settings petCodexThreadResponse, workspace, persona, toolFingerprint string) error {
	projectID = strings.TrimSpace(projectID)
	petID = strings.TrimSpace(petID)
	session := PetCodexSession{
		PetID:              petID,
		ThreadID:           settings.Thread.ID,
		Workspace:          workspace,
		PersonaFingerprint: petCodexPersonaFingerprint(persona),
		ToolFingerprint:    strings.TrimSpace(toolFingerprint),
		ProtocolVersion:    PetCodexPlanProtocolVersion,
		UpdatedAt:          time.Now().UnixMilli(),
	}
	if projectID != "" {
		if r == nil || r.agentSessions == nil {
			return errors.New("agent Codex session repository is unavailable")
		}
		return r.agentSessions.SaveAgentCodexSession(ctx, AgentCodexSession{
			ProjectID:          projectID,
			ThreadID:           session.ThreadID,
			Workspace:          session.Workspace,
			PersonaFingerprint: session.PersonaFingerprint,
			ToolFingerprint:    session.ToolFingerprint,
			ProtocolVersion:    session.ProtocolVersion,
			UpdatedAt:          session.UpdatedAt,
		})
	}
	if r == nil || r.sessions == nil {
		return errors.New("pet Codex session repository is unavailable")
	}
	return r.sessions.SaveCodexSession(ctx, session)
}

func (r *PetCodexRuntime) ensureSession(
	ctx context.Context,
	state *petCodexPetState,
	workspace, projectID, petID, persona string,
	modelReference PetAgentModelReference,
	toolScope string,
	toolSnapshot PetCodexDynamicToolSnapshot,
) (*CodexAppServerClient, []string, error) {
	fingerprint := petCodexPersonaFingerprint(persona)
	modelID := strings.TrimSpace(modelReference.ModelID)
	toolScope = strings.TrimSpace(toolScope)
	toolFingerprint := strings.TrimSpace(toolSnapshot.Fingerprint)
	state.mu.Lock()
	current := state.client
	compatible := current != nil && samePetCodexWorkspace(state.workspace, workspace) &&
		state.personaFingerprint == fingerprint && state.protocolVersion == PetCodexPlanProtocolVersion &&
		state.toolScope == toolScope && state.toolFingerprint == toolFingerprint &&
		(modelID == "" || strings.TrimSpace(state.model) == modelID)
	state.mu.Unlock()
	if compatible {
		return current, nil, nil
	}
	if current != nil {
		state.mu.Lock()
		if state.client == current {
			state.client = nil
		}
		state.mu.Unlock()
		// 旧 workspace/persona 不再复用，但关闭进程不能放在 state.mu 内，
		// 否则 reader 的退出回调会等待同一把锁。
		_ = current.Close()
	}

	if r.isClosed() {
		return nil, nil, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}

	loaded, err := r.loadSession(ctx, projectID, petID)
	if err != nil {
		return nil, nil, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, err)
	}
	threadID := ""
	if loaded.session != nil && samePetCodexWorkspace(loaded.session.Workspace, workspace) &&
		loaded.session.PersonaFingerprint == fingerprint && loaded.session.ToolFingerprint == toolFingerprint &&
		loaded.session.ProtocolVersion == PetCodexPlanProtocolVersion {
		threadID = strings.TrimSpace(loaded.session.ThreadID)
	}
	client, err := NewCodexAppServerClient(CodexAppServerClientOptions{
		Executable:            r.executable,
		CommandFactory:        r.commandFactory,
		WorkingDirectory:      workspace,
		ResponseTimeout:       r.responseTimeout,
		ServerRequestHandler:  r.handleCodexServerRequest,
		ServerRequestObserver: r.observeCodexServerRequest,
	})
	if err != nil {
		return nil, nil, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, err)
	}
	if !r.attachClient(state, client) {
		_ = client.Close()
		return nil, nil, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	go r.consumeClient(state, client)

	if _, err := client.Call(ctx, "initialize", codexPetAppServerInitializeParams(
		"code-switch-pet",
		"Code Switch Pet",
		"pet-runtime",
	)); err != nil {
		r.detachClient(state, client)
		_ = client.Close()
		return nil, nil, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, err)
	}
	if err := client.Notify("initialized", map[string]any{}); err != nil {
		r.detachClient(state, client)
		_ = client.Close()
		return nil, nil, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, err)
	}

	method := "thread/start"
	params := r.threadStartParamsWithModel(workspace, persona, modelReference, toolSnapshot)
	if threadID != "" {
		method = "thread/resume"
		params = r.threadResumeParamsWithModel(threadID, workspace, persona, modelReference)
	}
	response, err := client.Call(ctx, method, params)
	if err != nil {
		r.detachClient(state, client)
		_ = client.Close()
		return nil, nil, newPetAIError(PET_AI_UPSTREAM_ERROR, 0, err)
	}
	settings, err := parseAndVerifyPetCodexThreadResponse(response, workspace, threadID)
	if err != nil {
		r.detachClient(state, client)
		_ = client.Close()
		return nil, nil, newPetAIError(PET_AI_RESPONSE_INVALID, 0, err)
	}
	staleTurnIDs := []string(nil)
	if threadID != "" {
		staleTurnIDs, err = r.interruptStaleTurn(ctx, client, settings)
		if err != nil {
			r.detachClient(state, client)
			_ = client.Close()
			return nil, nil, newPetAIError(PET_AI_UPSTREAM_ERROR, 0, err)
		}
	}
	if err := r.saveSession(ctx, projectID, petID, settings, workspace, persona, toolFingerprint); err != nil {
		r.detachClient(state, client)
		_ = client.Close()
		return nil, nil, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, err)
	}
	state.mu.Lock()
	owned := state.client == client
	if owned {
		state.threadID = settings.Thread.ID
		state.workspace = workspace
		state.personaFingerprint = fingerprint
		state.protocolVersion = PetCodexPlanProtocolVersion
		state.toolScope = toolScope
		state.toolFingerprint = toolFingerprint
		state.toolNames = petCodexToolNames(toolSnapshot.Definitions)
		state.modelProvider = settings.ModelProvider
		state.model = firstNonEmptyPetAIString(settings.Model, modelID)
	}
	state.mu.Unlock()
	if !owned {
		_ = client.Close()
		return nil, nil, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	return client, staleTurnIDs, nil
}

func (r *PetCodexRuntime) threadStartParams(workspace, persona string, snapshots ...PetCodexDynamicToolSnapshot) map[string]any {
	return r.threadStartParamsWithModel(workspace, persona, PetAgentModelReference{}, snapshots...)
}

func (r *PetCodexRuntime) threadStartParamsWithModel(workspace, persona string, modelReference PetAgentModelReference, snapshots ...PetCodexDynamicToolSnapshot) map[string]any {
	params := map[string]any{
		// cwd 是宠物绑定项目的安全边界，不是对 Codex 全局配置的覆盖。
		"cwd":          workspace,
		"threadSource": "user",
	}
	if model := strings.TrimSpace(modelReference.ModelID); model != "" {
		params["model"] = model
	}
	if strings.TrimSpace(persona) != "" {
		params["developerInstructions"] = persona
	}
	if len(snapshots) > 0 && len(snapshots[0].Definitions) > 0 {
		params["dynamicTools"] = petCodexDynamicToolDefinitions(snapshots[0].Definitions)
	}
	return params
}

func (r *PetCodexRuntime) threadResumeParams(threadID, workspace, persona string) map[string]any {
	return r.threadResumeParamsWithModel(threadID, workspace, persona, PetAgentModelReference{})
}

func (r *PetCodexRuntime) threadResumeParamsWithModel(threadID, workspace, persona string, modelReference PetAgentModelReference) map[string]any {
	params := map[string]any{
		"threadId":     threadID,
		"cwd":          workspace,
		"excludeTurns": true,
		"initialTurnsPage": map[string]any{
			"limit":         1,
			"sortDirection": "desc",
			"itemsView":     "summary",
		},
	}
	if model := strings.TrimSpace(modelReference.ModelID); model != "" {
		params["model"] = model
	}
	if strings.TrimSpace(persona) != "" {
		params["developerInstructions"] = persona
	}
	return params
}

func (r *PetCodexRuntime) buildTurnStartParams(active *petCodexActiveTurn) map[string]any {
	input := make([]map[string]any, 0, len(active.request.Skills)+len(active.request.Images)+len(active.request.LocalImages)+1)
	for _, skill := range active.request.Skills {
		input = append(input, map[string]any{
			// Skill 必须作为 Codex 原生 input item 发送，拼进 prompt 只会让
			// 模型看到路径文本，却不会加载该 Skill 的 SKILL.md 语义。
			"type": "skill",
			"name": skill.Name,
			"path": skill.Path,
		})
	}
	turnText := buildPetCodexTurnText(active.request.RuntimeContext, active.request.UserText)
	if turnText != "" {
		input = append(input, map[string]any{"type": "text", "text": turnText})
	}
	for _, image := range active.request.Images {
		input = append(input, map[string]any{
			"type": "image",
			"url":  "data:" + image.MediaType + ";base64," + image.Data,
		})
	}
	for _, image := range active.request.LocalImages {
		input = append(input, map[string]any{
			"type": "localImage",
			"path": image.Path,
		})
	}
	active.state.mu.Lock()
	threadID := active.state.threadID
	workspace := active.state.workspace
	active.state.mu.Unlock()
	params := map[string]any{
		"threadId":            threadID,
		"clientUserMessageId": active.request.RequestID,
		"input":               input,
		// turn 的 cwd 只用于把当前宠物请求绑定到已解析的项目目录；审批、
		// sandbox、网络和 roots 均交回 Codex CLI 默认配置决定。
		"cwd": workspace,
	}
	if model := strings.TrimSpace(active.modelReference.ModelID); model != "" {
		params["model"] = model
	}
	if effort := strings.TrimSpace(string(active.modelReference.ReasoningEffort)); effort != "" {
		params["effort"] = effort
	}
	return params
}

func buildPetCodexTurnText(runtimeContext, userText string) string {
	runtimeContext = strings.TrimSpace(runtimeContext)
	userText = strings.TrimSpace(userText)
	parts := make([]string, 0, 2)
	if runtimeContext != "" {
		parts = append(parts, "<pet-runtime-context>\n"+runtimeContext+"\n</pet-runtime-context>")
	}
	if userText != "" {
		parts = append(parts, "<pet-user-message>\n"+userText+"\n</pet-user-message>")
	}
	return strings.Join(parts, "\n\n")
}

func parseAndVerifyPetCodexThreadResponse(raw json.RawMessage, workspace, resumedThreadID string) (petCodexThreadResponse, error) {
	var response petCodexThreadResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return response, err
	}
	response.Thread.ID = strings.TrimSpace(response.Thread.ID)
	response.CWD = strings.TrimSpace(response.CWD)
	threadCWD := strings.TrimSpace(response.Thread.CWD)
	if threadCWD == "" {
		threadCWD = response.CWD
	}
	if response.Thread.ID == "" || threadCWD == "" || !samePetCodexWorkspace(threadCWD, workspace) {
		return response, errors.New("Codex thread 返回的 cwd 或 thread id 不符合预期")
	}
	if response.CWD != "" && !samePetCodexWorkspace(response.CWD, workspace) {
		return response, errors.New("Codex thread 返回了不属于宠物项目的 cwd")
	}
	if resumedThreadID != "" && response.Thread.ID != strings.TrimSpace(resumedThreadID) {
		return response, errors.New("Codex resume 返回了不同的 thread id")
	}
	return response, nil
}

func (r *PetCodexRuntime) interruptStaleTurn(ctx context.Context, client *CodexAppServerClient, response petCodexThreadResponse) ([]string, error) {
	turns := response.Thread.Turns
	if len(turns) == 0 && response.InitialTurnsPage != nil {
		// 旧版 fixture/兼容层仍把 resume 的摘要放在 initialTurnsPage；保留
		// 这个读取分支，但不再把它当成唯一的 Codex 返回形态。
		turns = make([]struct {
			ID     string            `json:"id"`
			Status string            `json:"status"`
			Items  []json.RawMessage `json:"items"`
		}, len(response.InitialTurnsPage.Data))
		for index, turn := range response.InitialTurnsPage.Data {
			turns[index].ID = turn.ID
			turns[index].Status = turn.Status
		}
	}
	staleTurnIDs := make([]string, 0, len(turns))
	for index := len(turns) - 1; index >= 0; index-- {
		turn := turns[index]
		if normalizePetCodexTurnStatus(turn.Status) != "in_progress" {
			continue
		}
		turnID := strings.TrimSpace(turn.ID)
		if turnID == "" {
			return nil, errors.New("Codex resume 返回了无 ID 的活动 turn")
		}
		_, err := client.Call(ctx, "turn/interrupt", map[string]any{
			"threadId": response.Thread.ID,
			"turnId":   turnID,
		})
		if err != nil {
			return nil, err
		}
		staleTurnIDs = append(staleTurnIDs, turnID)
	}
	return staleTurnIDs, nil
}

func (r *PetCodexRuntime) interruptTurn(active *petCodexActiveTurn) {
	if active == nil || active.state == nil {
		return
	}
	active.state.mu.Lock()
	turnID := active.turnID
	client := active.client
	threadID := active.state.threadID
	if turnID == "" || client == nil || threadID == "" || active.interruptSent {
		active.state.mu.Unlock()
		return
	}
	active.interruptSent = true
	active.state.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), r.responseTimeout)
		defer cancel()
		_, _ = client.Call(ctx, "turn/interrupt", map[string]any{
			"threadId": threadID,
			"turnId":   turnID,
		})
	}()
}

func (r *PetCodexRuntime) consumeClient(state *petCodexPetState, client *CodexAppServerClient) {
	for {
		select {
		case message, ok := <-client.Notifications():
			if !ok {
				r.handleClientExit(state, client)
				return
			}
			r.handleNotification(state, client, message)
		case <-client.Done():
			// stdout 中排在退出之前的通知必须先处理，尤其是 completed 和最后一条 usage。
			for {
				select {
				case message, ok := <-client.Notifications():
					if !ok {
						r.handleClientExit(state, client)
						return
					}
					r.handleNotification(state, client, message)
				default:
					r.handleClientExit(state, client)
					return
				}
			}
		}
	}
}

func (r *PetCodexRuntime) handleClientExit(state *petCodexPetState, client *CodexAppServerClient) {
	state.mu.Lock()
	if state.client != client {
		state.mu.Unlock()
		return
	}
	state.client = nil
	active := state.active
	state.active = nil
	startCancel := context.CancelFunc(nil)
	if active != nil {
		startCancel = active.startCancel
		active.startCancel = nil
	}
	cancelled := active != nil && active.cancelled
	state.mu.Unlock()
	if startCancel != nil {
		startCancel()
	}
	writePetCodexDiagnostic(
		"pet-codex-client-exit",
		fmt.Sprintf("pet_id=%q", state.petID),
		fmt.Sprintf("has_active=%t", active != nil),
	)
	if active == nil {
		return
	}
	r.removeRequest(active.request.RequestID, active)
	if cancelled {
		r.finishActivity(active, PetActivityCancelled)
		r.emitEvent(active, PetAIEvent{Type: PetAIEventCancelled})
		return
	}
	r.finishActivity(active, PetActivityFailed)
	r.emitEvent(active, PetAIEvent{
		Type:  PetAIEventFailed,
		Error: &PetAIEventError{Code: string(PET_AI_DEPENDENCY_UNAVAILABLE)},
	})
}

func (r *PetCodexRuntime) handleNotification(state *petCodexPetState, client *CodexAppServerClient, message CodexAppServerMessage) {
	if r == nil || r.isClosed() {
		return
	}
	state.mu.Lock()
	active := state.active
	if active == nil || active.client != client {
		state.mu.Unlock()
		return
	}
	requestID := active.request.RequestID
	state.mu.Unlock()
	if message.Method == "turn/started" || message.Method == "item/agentMessage/delta" ||
		message.Method == "item/completed" || message.Method == "turn/completed" {
		writePetCodexDiagnostic(
			"pet-codex-notification",
			fmt.Sprintf("pet_id=%q", state.petID),
			fmt.Sprintf("request_id=%q", requestID),
			fmt.Sprintf("method=%q", message.Method),
			fmt.Sprintf("params_bytes=%d", len(message.Params)),
		)
	}
	// 日志写盘必须在状态锁外完成；重新拿锁后复核 active，避免日志期间
	// 旧 turn 已完成并被下一轮替换时，把通知误应用到新 owner。
	state.mu.Lock()
	if state.active != active || active.client != client {
		state.mu.Unlock()
		return
	}
	params := message.Params
	switch message.Method {
	case "turn/started":
		var value petCodexTurnStartedNotification
		if json.Unmarshal(params, &value) != nil {
			state.mu.Unlock()
			return
		}
		turnID := firstNonEmptyPetAIString(value.TurnID, value.Turn.ID)
		if !r.matchesTurn(active, value.ThreadID, turnID) {
			state.mu.Unlock()
			return
		}
		if active.turnID == "" {
			active.turnID = strings.TrimSpace(turnID)
		}
		cancelled := active.cancelled
		state.mu.Unlock()
		if cancelled {
			r.interruptTurn(active)
		}
	case "item/agentMessage/delta":
		var value petCodexDeltaNotification
		if json.Unmarshal(params, &value) != nil || !r.matchesTurn(active, value.ThreadID, value.TurnID) {
			state.mu.Unlock()
			return
		}
		if strings.TrimSpace(value.Delta) == "" {
			state.mu.Unlock()
			return
		}
		active.turnID = firstNonEmptyPetAIString(active.turnID, value.TurnID)
		active.text.WriteString(value.Delta)
		activity := active.activity
		event := r.nextEventLocked(active, PetAIEvent{Type: PetAIEventDelta, Delta: value.Delta})
		state.mu.Unlock()
		// 活动态 emitter 属于 UI 旁路，不能在状态锁内执行；否则 Wails/SSE
		// 广播的慢路径会把后续 completed 通知一起堵住。
		if activity != nil {
			activity.Output()
		}
		_ = r.emit(event)
	case "item/completed":
		var value petCodexItemCompletedNotification
		if json.Unmarshal(params, &value) != nil || !r.matchesTurn(active, value.ThreadID, value.TurnID) {
			state.mu.Unlock()
			return
		}
		itemType := petCodexItemType(value.Item)
		text := petCodexAssistantMessageText(value.Item)
		if text != "" {
			// item/completed 携带的是完整 assistant 文本，优先级高于此前可能
			// 收到的 delta，避免把增量和全文拼成重复回复。
			active.completedText = text
		}
		writePetCodexDiagnostic(
			"pet-codex-item-completed",
			fmt.Sprintf("pet_id=%q", state.petID),
			fmt.Sprintf("request_id=%q", active.request.RequestID),
			fmt.Sprintf("item_type=%q", itemType),
			fmt.Sprintf("text_bytes=%d", len(text)),
		)
		// 工具、推理和 assistant item 都可能只在完成时有通知，期间没有
		// agentMessage/delta；把这个协议级进展透传给前端，避免长工具链被
		// 固定的 UI 空闲计时器误判为失联。事件不携带正文，不改变聊天显示。
		progressEvent := r.nextEventLocked(active, PetAIEvent{Type: PetAIEventProgress})
		state.mu.Unlock()
		_ = r.emit(progressEvent)
	case "thread/tokenUsage/updated":
		var value petCodexUsageNotification
		if json.Unmarshal(params, &value) != nil || !r.matchesTurn(active, value.ThreadID, value.TurnID) {
			state.mu.Unlock()
			return
		}
		usage := petCodexUsageToSnapshot(value)
		active.usage = mergePetAIUsage(active.usage, usage)
		active.usageSeen = active.usage.InputTokens > 0 || active.usage.OutputTokens > 0
		state.mu.Unlock()
	case "turn/completed":
		var startCancel context.CancelFunc
		var value petCodexTurnCompletedNotification
		if json.Unmarshal(params, &value) != nil {
			state.mu.Unlock()
			return
		}
		turnID := firstNonEmptyPetAIString(value.TurnID, value.Turn.ID)
		if !r.matchesTurn(active, value.ThreadID, turnID) {
			state.mu.Unlock()
			return
		}
		if active.turnID == "" {
			active.turnID = turnID
		}
		state.active = nil
		// app-server 可能在 turn/start response 之前先发 completed；取消仍在
		// 等待的启动 RPC，避免它拖到 response timeout 后才释放旧 pending。
		startCancel = active.startCancel
		active.startCancel = nil
		usage := active.usage
		usageSeen := active.usageSeen
		text := firstNonEmptyPetAIString(
			active.completedText,
			petCodexAssistantMessageTextFromItems(value.Turn.Items),
			active.text.String(),
		)
		status := normalizePetCodexTurnStatus(firstNonEmptyPetAIString(value.Turn.Status, "completed"))
		cancelled := active.cancelled
		writePetCodexDiagnostic(
			"pet-codex-turn-terminal",
			fmt.Sprintf("pet_id=%q", state.petID),
			fmt.Sprintf("request_id=%q", active.request.RequestID),
			fmt.Sprintf("status=%q", status),
			fmt.Sprintf("text_bytes=%d", len(text)),
			fmt.Sprintf("turn_known=%t", active.turnID != ""),
		)
		var events []PetAIEvent
		if status == "interrupted" || cancelled {
			events = append(events, r.nextEventLocked(active, PetAIEvent{Type: PetAIEventCancelled}))
		} else if status != "completed" {
			events = append(events, r.nextEventLocked(active, PetAIEvent{
				Type:  PetAIEventFailed,
				Error: &PetAIEventError{Code: string(PET_AI_UPSTREAM_ERROR)},
			}))
		} else {
			if usageSeen {
				events = append(events, r.nextEventLocked(active, PetAIEvent{
					Type:  PetAIEventUsage,
					Usage: petCodexUsagePayload(active, usage),
				}))
			}
			events = append(events, r.nextEventLocked(active, PetAIEvent{
				Type: PetAIEventCompleted,
				Text: text,
			}))
		}
		state.mu.Unlock()
		if startCancel != nil {
			startCancel()
		}
		r.removeRequest(active.request.RequestID, active)
		if status == "interrupted" || cancelled {
			r.finishActivity(active, PetActivityCancelled)
		} else if status != "completed" {
			r.finishActivity(active, PetActivityFailed)
		} else {
			r.finishActivity(active, PetActivityCompleted)
		}
		for _, event := range events {
			_ = r.emit(event)
		}
	default:
		// app-server 会持续扩展通知类型，未知通知对宠物回复没有业务含义；
		// 必须在这里释放状态锁，否则第一条新通知就会永久阻塞后续 turn。
		state.mu.Unlock()
	}
}

func (r *PetCodexRuntime) matchesTurn(active *petCodexActiveTurn, threadID, turnID string) bool {
	if strings.TrimSpace(threadID) != "" && strings.TrimSpace(threadID) != active.state.threadID {
		return false
	}
	turnID = strings.TrimSpace(turnID)
	if turnID != "" {
		if _, stale := active.staleTurnIDs[turnID]; stale {
			// 旧恢复 turn 的终态不能结束当前请求；即使它与当前 thread 相同，
			// turn ID 仍然是 Codex 区分两轮请求的唯一业务边界。
			return false
		}
	}
	return active.turnID == "" || turnID == "" || active.turnID == turnID
}

func petCodexItemType(raw json.RawMessage) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(jsonStringField(fields, "type")))
}

func petCodexAssistantMessageText(raw json.RawMessage) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ""
	}
	typeName := strings.ToLower(strings.TrimSpace(jsonStringField(fields, "type")))
	role := strings.ToLower(strings.TrimSpace(jsonStringField(fields, "role")))
	if nested, ok := fields["agentMessage"]; ok {
		if text := petCodexAssistantMessageText(nested); text != "" {
			return text
		}
		// 某些 app-server 版本把完整消息放在 item.agentMessage 下，
		// nested 对象本身不带 type/role，只能按消息载荷读取正文。
		var nestedFields map[string]json.RawMessage
		if json.Unmarshal(nested, &nestedFields) == nil {
			if text := petCodexHistoryText(nestedFields); strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	if typeName != "agentmessage" && typeName != "agent_message" &&
		typeName != "assistant" && typeName != "assistantmessage" &&
		typeName != "assistant_message" && role != "assistant" {
		return ""
	}
	return petCodexHistoryText(fields)
}

func petCodexAssistantMessageTextFromItems(items []json.RawMessage) string {
	text := ""
	for _, item := range items {
		if candidate := petCodexAssistantMessageText(item); strings.TrimSpace(candidate) != "" {
			// turn.items 可能包含多个 assistant item；最后一条才是该 turn
			// 的最终可见回复，工具和中间消息不应覆盖它。
			text = candidate
		}
	}
	return text
}

func (r *PetCodexRuntime) nextEventLocked(active *petCodexActiveTurn, event PetAIEvent) PetAIEvent {
	active.sequence++
	event.PetID = active.request.PetID
	event.RequestID = active.request.RequestID
	event.Sequence = active.sequence
	event.ProjectID = active.request.ProjectID
	event.Source = active.request.Source
	event.ChannelInstanceID = active.request.ChannelInstanceID
	event.ChannelChatID = active.request.ChannelChatID
	return event
}

func (r *PetCodexRuntime) emitActiveEvent(active *petCodexActiveTurn, event PetAIEvent) error {
	active.state.mu.Lock()
	value := r.nextEventLocked(active, event)
	active.state.mu.Unlock()
	return r.emit(value)
}

func (r *PetCodexRuntime) emitEvent(active *petCodexActiveTurn, event PetAIEvent) error {
	return r.emitActiveEvent(active, event)
}

func (r *PetCodexRuntime) emit(event PetAIEvent) error {
	if r == nil || r.emitter == nil {
		return nil
	}
	if err := r.emitter.Emit(event); err != nil {
		return newPetAIError(PET_AI_EVENT_ERROR, 0, err)
	}
	return nil
}

func (r *PetCodexRuntime) failStartedTurn(active *petCodexActiveTurn, code PetAIErrorCode, cancelled bool) error {
	if active != nil {
		writePetCodexDiagnostic(
			"pet-codex-turn-terminal",
			fmt.Sprintf("pet_id=%q", active.request.PetID),
			fmt.Sprintf("request_id=%q", active.request.RequestID),
			fmt.Sprintf("code=%q", code),
			fmt.Sprintf("cancelled=%t", cancelled),
		)
	}
	if cancelled {
		r.finishActivity(active, PetActivityCancelled)
		_ = r.emitActiveEvent(active, PetAIEvent{Type: PetAIEventCancelled})
		return newPetAIError(PET_AI_REQUEST_CANCELLED, 0, nil)
	}
	r.finishActivity(active, PetActivityFailed)
	_ = r.emitActiveEvent(active, PetAIEvent{
		Type:  PetAIEventFailed,
		Error: &PetAIEventError{Code: string(code)},
	})
	return newPetAIError(code, 0, nil)
}

func (r *PetCodexRuntime) releaseActiveForStartFailure(active *petCodexActiveTurn) petCodexStartFailure {
	if active == nil || active.state == nil {
		return petCodexStartFailure{}
	}
	active.state.mu.Lock()
	defer active.state.mu.Unlock()
	if active.state.active != active {
		return petCodexStartFailure{}
	}
	active.state.active = nil
	startCancel := active.startCancel
	active.startCancel = nil
	return petCodexStartFailure{
		owned:       true,
		client:      active.client,
		threadID:    active.state.threadID,
		turnID:      active.turnID,
		cancelled:   active.cancelled,
		startCancel: startCancel,
	}
}

func (r *PetCodexRuntime) finishActivity(active *petCodexActiveTurn, phase PetActivityPhase) {
	if active == nil || active.activity == nil {
		return
	}
	active.activity.Finish(phase)
}

func (r *PetCodexRuntime) removeRequest(requestID string, active *petCodexActiveTurn) {
	r.mu.Lock()
	if r.requests[requestID] == active {
		delete(r.requests, requestID)
	}
	r.mu.Unlock()
}

func (r *PetCodexRuntime) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

func petCodexPersonaFingerprint(persona string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(persona)))
	return hex.EncodeToString(digest[:])
}

func samePetCodexWorkspace(left, right string) bool {
	left = filepath.Clean(strings.TrimSpace(left))
	right = filepath.Clean(strings.TrimSpace(right))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func containsPetCodexWorkspace(values []string, workspace string) bool {
	for _, value := range values {
		if samePetCodexWorkspace(value, workspace) {
			return true
		}
	}
	return false
}

func normalizePetCodexTurnStatus(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	// app-server v2 使用 camelCase（inProgress），而旧 fixture/兼容层常用
	// snake_case；统一后 stale turn 和 completed 分支才不会漏判。
	value = strings.ReplaceAll(value, "inprogress", "in_progress")
	return value
}

func petCodexUsageToSnapshot(value petCodexUsageNotification) modelpricing.UsageSnapshot {
	last := value.TokenUsage.Last
	return modelpricing.UsageSnapshot{
		InputTokens:       clampPetCodexTokenInt(last.InputTokens),
		OutputTokens:      clampPetCodexTokenInt(last.OutputTokens),
		ReasoningTokens:   clampPetCodexTokenInt(last.ReasoningTokens),
		CacheCreateTokens: clampPetCodexTokenInt(last.CacheWriteTokens),
		CacheReadTokens:   clampPetCodexTokenInt(last.CachedInputTokens),
	}
}

func clampPetCodexTokenInt(value int64) int {
	if value <= 0 {
		return 0
	}
	maxInt := int64(^uint(0) >> 1)
	if value > maxInt {
		return int(maxInt)
	}
	return int(value)
}

func petCodexUsagePayload(active *petCodexActiveTurn, usage modelpricing.UsageSnapshot) *PetStreamUsagePayload {
	return &PetStreamUsagePayload{
		ID:                active.request.RequestID,
		At:                time.Now().UnixMilli(),
		Provider:          firstNonEmptyPetAIString(active.state.modelProvider, "codex"),
		Model:             active.state.model,
		InputTokens:       usage.InputTokens,
		OutputTokens:      usage.OutputTokens,
		ReasoningTokens:   usage.ReasoningTokens,
		CacheCreateTokens: usage.CacheCreateTokens,
		CacheReadTokens:   usage.CacheReadTokens,
		ServiceTier:       string(usage.ServiceTier),
	}
}
