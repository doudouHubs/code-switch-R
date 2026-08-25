package channels

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"codeswitch/services"
	"github.com/google/uuid"
)

// ProjectWorkspaceResolver 是频道项目绑定到当前项目事实源的窄接口。
// runtime 不直接读取 ProjectManagerService，避免把项目扫描、缓存和频道会话耦合到一起。
type ProjectWorkspaceResolver func(context.Context, string) (string, error)

// ChannelProviderResolver 是频道 Agent 解析默认 provider 的窄接口。
// OpenCowork 允许频道只保存 provider/model 覆盖项，真正的全局 provider 仍由宿主
// 负责读取；runtime 只接收已经归一化的引用，不接触 provider 凭据或配置文件。
type ChannelProviderResolver func(context.Context, ChannelInstance) (services.PetProviderReference, error)

// AgentRuntime 把 provider 入站消息接到现有 PetAI 协议执行器。
// 它使用独立 PetAIService 实例，因此频道的 request、事件和 workspace resolver 不会
// 污染桌宠默认服务，也不会把频道事件误计入桌宠记忆或经验。
type AgentRuntime struct {
	store          *Store
	manager        *Manager
	projectResolve ProjectWorkspaceResolver
	ai             *services.PetAIService

	mu               sync.Mutex
	runs             map[string]*channelAgentRun
	chatLocks        map[string]*sync.Mutex
	eventSink        EventSink
	providerResolver ChannelProviderResolver
}

type channelAgentRun struct {
	requestID string
	instance  ChannelInstance
	session   ChannelSession
	incoming  ChannelMessage
	text      strings.Builder

	streamMu sync.Mutex
	stream   StreamingHandle
	closed   bool
	chatLock *sync.Mutex
}

// NewAgentRuntime 创建频道专用 Agent runtime。
// ProviderReader 和 Transport 直接复用主进程已有实现，协议解析、SSE 和 continuation
// 仍只有 PetAIService 一个 owner；频道只负责上下文路由与回复目标。
func NewAgentRuntime(
	store *Store,
	manager *Manager,
	projectResolve ProjectWorkspaceResolver,
	providerReader services.PetAIProviderReader,
	transport services.PetAIHTTPTransport,
	eventSink EventSink,
	providerResolvers ...ChannelProviderResolver,
) *AgentRuntime {
	var providerResolver ChannelProviderResolver
	if len(providerResolvers) > 0 {
		providerResolver = providerResolvers[0]
	}
	runtime := &AgentRuntime{
		store:            store,
		manager:          manager,
		projectResolve:   projectResolve,
		runs:             make(map[string]*channelAgentRun),
		chatLocks:        make(map[string]*sync.Mutex),
		eventSink:        eventSink,
		providerResolver: providerResolver,
	}
	runtime.ai = services.NewPetAIServiceWithDependencies(services.PetAIDependencies{
		ProviderReader:    providerReader,
		Transport:         transport,
		WorkspaceResolver: runtime,
		ToolDefinitions: func(scope string) []services.PetAgentToolDefinition {
			instanceID, _, _, err := parseChannelToolScope(scope)
			if err != nil || store == nil {
				return nil
			}
			instance, found, getErr := store.GetInstance(instanceID)
			if getErr != nil || !found || instance.Archived {
				return nil
			}
			return channelToolDefinitionsForInstance(instance)
		},
		ToolExecutorFactory: func(ctx context.Context, scope, workspace string) (services.PetAgentToolRunner, error) {
			instanceID, sessionID, chatID, err := parseChannelToolScope(scope)
			if err != nil {
				return nil, err
			}
			if store == nil {
				return nil, errors.New("channel store is unavailable")
			}
			instance, found, err := store.GetInstance(instanceID)
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, errors.New("channel instance not found")
			}
			if instance.Archived {
				return nil, errors.New("archived channel is read-only")
			}
			if sessionID == "" || chatID == "" {
				return nil, errors.New("channel tool session scope is incomplete")
			}
			return newChannelAgentToolExecutor(store, manager, eventSink, instance, sessionID, chatID, workspace)
		},
		Emitter: services.PetAIEventEmitterFunc(func(event services.PetAIEvent) error {
			return runtime.Emit(event)
		}),
	})
	return runtime
}

// Resolve 实现 PetWorkspaceResolver。session 只作为路由身份，真正 workspace 每次
// 都从当前 instance.ProjectID 重新解析；这样频道换绑项目后不会继续沿用旧目录。
func (r *AgentRuntime) Resolve(ctx context.Context, sessionID string) (string, error) {
	if r == nil || r.store == nil {
		return "", errors.New("channel runtime is unavailable")
	}
	session, found, err := r.store.GetSessionByID(sessionID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("channel session not found")
	}
	instance, found, err := r.store.GetInstance(session.InstanceID)
	if err != nil {
		return "", err
	}
	if !found || instance.ProjectID == nil || strings.TrimSpace(*instance.ProjectID) == "" {
		return "", errors.New("channel project binding is missing")
	}
	projectID := strings.TrimSpace(*instance.ProjectID)
	if r.projectResolve != nil {
		workspace, err := r.projectResolve(ctx, projectID)
		return normalizeWorkspace(ctx, workspace, err)
	}
	// ProjectSummary.ID 当前就是规范化项目路径；只有测试替身或旧数据没有 resolver
	// 时才允许直接使用它，并且仍必须经过绝对路径、目录和 symlink 校验。
	return normalizeWorkspace(ctx, projectID)
}

func normalizeWorkspace(ctx context.Context, value string, resolveErr ...error) (string, error) {
	if len(resolveErr) > 0 && resolveErr[0] != nil {
		return "", resolveErr[0]
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("channel workspace is empty")
	}
	path, err := filepath.Abs(value)
	if err != nil {
		return "", errors.New("channel workspace is invalid")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", errors.New("channel workspace is unavailable")
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", errors.New("channel workspace cannot be resolved")
	}
	return filepath.Clean(path), nil
}

// HandleEvent 是 manager 的唯一入站出口。状态事件只向 UI 转发，真正的 Agent 任务
// 必须先过 enabled/autoReply/去重/session 三道门，再启动模型请求。
func (r *AgentRuntime) HandleEvent(event ChannelEvent) {
	if r == nil {
		return
	}
	if event.Type == "incoming_message" {
		message, ok := event.Data.(ChannelMessage)
		if ok {
			go r.handleIncoming(message)
		}
	}
	r.publish(event)
}

func (r *AgentRuntime) publish(event ChannelEvent) {
	if r != nil && r.eventSink != nil {
		r.eventSink(event)
	}
}

func (r *AgentRuntime) handleIncoming(message ChannelMessage) {
	if r.store == nil || r.manager == nil {
		return
	}
	instance, found, err := r.store.GetInstance(message.InstanceID)
	if err != nil || !found || instance.Archived || !instance.Enabled || !instance.Features.AutoReply {
		return
	}
	if instance.ProjectID == nil || strings.TrimSpace(*instance.ProjectID) == "" {
		return
	}

	chatKey := instance.ID + "\x00" + message.ChatID
	chatLock := r.lockForChat(chatKey)
	chatLock.Lock()
	keepLock := false
	// 从这里到 AI 终态都持有同一聊天锁，确保同一会话历史按入站顺序推进。
	defer func() {
		if !keepLock {
			chatLock.Unlock()
		}
	}()

	workspace, err := r.Resolve(context.Background(), r.sessionKey(instance.ID, message.ChatID))
	if err != nil {
		// session 尚未创建时按当前项目直接解析；Resolve 的 session 复核仍会在
		// Upsert 后接管后续请求，避免把一个失效项目绑定写入历史。
		workspace, err = r.resolveProjectWorkspace(context.Background(), *instance.ProjectID)
	}
	if err != nil {
		r.publishError(instance, err)
		return
	}

	session, err := r.ensureSession(instance, message, workspace)
	if err != nil {
		r.publishError(instance, err)
		return
	}
	message.SessionID = session.ID
	if message.ID == "" {
		message.ID = sessionKey(message.InstanceID, message.ExternalID, fmt.Sprint(message.Timestamp), message.Role)
	}
	inserted, err := r.store.AppendMessageIfNew(message)
	if err != nil || !inserted {
		// 外部 ID 去重必须发生在启动 Agent 之前；重复 webhook 不应再次消耗模型额度。
		if err != nil {
			r.publishError(instance, err)
		}
		return
	}
	// 入站事件先于 Agent 异步处理到达 UI；持久化完成后再补一条标准 message
	// 事件，页面刷新历史时不会撞上尚未写入的竞态。
	r.publish(ChannelEvent{Type: "message", InstanceID: instance.ID, PluginType: instance.Type, Data: message, At: nowMillis()})
	for _, media := range message.Images {
		_ = r.store.SaveMedia(message.ID, message.InstanceID, media)
	}
	if message.Audio != nil {
		_ = r.store.SaveMedia(message.ID, message.InstanceID, *message.Audio)
	}

	history, err := r.historyForSession(session.ID, message.ID)
	if err != nil {
		r.publishError(instance, err)
		return
	}
	provider, err := r.resolveProviderReference(context.Background(), instance)
	if err != nil {
		r.publishError(instance, err)
		r.sendFailureMessage(instance, message.ChatID)
		return
	}
	requestID := "channel-" + uuid.NewString()
	run := &channelAgentRun{requestID: requestID, instance: instance, session: session, incoming: message, chatLock: chatLock}
	r.mu.Lock()
	r.runs[requestID] = run
	r.mu.Unlock()
	keepLock = true

	_, err = r.ai.StartChat(context.Background(), services.PetChatRequest{
		PetID:     session.ID,
		RequestID: requestID,
		Provider:  provider,
		Persona:   strings.TrimSpace(instance.Config["systemPrompt"]),
		UserText:  message.Content,
		Images:    channelImages(message.Images),
		History:   history,
		ToolScope: channelToolScope(instance.ID, session.ID, message.ChatID),
	})
	if err != nil {
		r.finishRun(run)
		r.publishError(instance, err)
		r.sendFailureMessage(instance, message.ChatID)
		return
	}
}

func (r *AgentRuntime) resolveProjectWorkspace(ctx context.Context, projectID string) (string, error) {
	if r.projectResolve != nil {
		workspace, err := r.projectResolve(ctx, projectID)
		return normalizeWorkspace(ctx, workspace, err)
	}
	return normalizeWorkspace(ctx, projectID)
}

func (r *AgentRuntime) sessionKey(instanceID, chatID string) string {
	return sessionKey(instanceID, chatID)
}

func (r *AgentRuntime) lockForChat(key string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	if lock := r.chatLocks[key]; lock != nil {
		return lock
	}
	lock := &sync.Mutex{}
	r.chatLocks[key] = lock
	return lock
}

func (r *AgentRuntime) ensureSession(instance ChannelInstance, message ChannelMessage, workspace string) (ChannelSession, error) {
	projectID := ""
	if instance.ProjectID != nil {
		projectID = strings.TrimSpace(*instance.ProjectID)
	}
	session, found, err := r.store.GetSession(instance.ID, message.ChatID)
	if err != nil {
		return ChannelSession{}, err
	}
	if !found {
		session = ChannelSession{ID: sessionKey(instance.ID, message.ChatID), InstanceID: instance.ID, ChatID: message.ChatID}
	}
	session.ChatName = firstNonEmpty(session.ChatName, message.ChatID)
	session.SenderID = message.SenderID
	session.SenderName = message.SenderName
	session.ProjectID = projectID
	session.WorkingFolder = workspace
	session.UpdatedAt = nowMillis()
	if err := r.store.UpsertSession(session); err != nil {
		return ChannelSession{}, err
	}
	return session, nil
}

func (r *AgentRuntime) historyForSession(sessionID, currentMessageID string) ([]services.PetAIMessage, error) {
	messages, err := r.store.ListMessages(sessionID, 200)
	if err != nil {
		return nil, err
	}
	history := make([]services.PetAIMessage, 0, len(messages))
	for _, message := range messages {
		if message.ID == currentMessageID || (message.Role != "user" && message.Role != "assistant") || (strings.TrimSpace(message.Content) == "" && len(message.Images) == 0) {
			continue
		}
		history = append(history, services.PetAIMessage{Role: message.Role, Content: message.Content, Images: channelImages(message.Images)})
	}
	return history, nil
}

func (r *AgentRuntime) resolveProviderReference(ctx context.Context, instance ChannelInstance) (services.PetProviderReference, error) {
	if r == nil || r.providerResolver == nil {
		return services.PetProviderReference{}, errors.New("channel default provider resolver is unavailable")
	}
	// 历史频道仍保留 providerPlatform/providerId/model 字段用于数据兼容，但这里故意
	// 不读取它们。模型和 Provider 的唯一运行时 owner 是客户端默认 Codex reader + Relay。
	reference, err := r.providerResolver(ctx, instance)
	if err != nil {
		return services.PetProviderReference{}, err
	}
	reference.Platform = strings.ToLower(strings.TrimSpace(reference.Platform))
	reference.ProviderID = strings.TrimSpace(reference.ProviderID)
	reference.Model = strings.TrimSpace(reference.Model)
	if reference.Platform == "" || reference.ProviderID == "" || reference.Model == "" {
		return services.PetProviderReference{}, errors.New("client default provider reference is incomplete")
	}
	reference.Capability = services.PetCapabilityChat
	return reference, nil
}

func channelImages(images []ChannelMedia) []services.PetAIImage {
	result := make([]services.PetAIImage, 0, len(images))
	for _, image := range images {
		if len(image.Data) == 0 || !strings.HasPrefix(strings.ToLower(image.MediaType), "image/") {
			continue
		}
		result = append(result, services.PetAIImage{Data: base64.StdEncoding.EncodeToString(image.Data), MediaType: strings.ToLower(image.MediaType)})
	}
	return result
}

func (r *AgentRuntime) Emit(event services.PetAIEvent) error {
	r.mu.Lock()
	run := r.runs[event.RequestID]
	r.mu.Unlock()
	if run == nil {
		return nil
	}
	switch event.Type {
	case services.PetAIEventDelta:
		run.text.WriteString(event.Delta)
		r.updateStreaming(run, strings.TrimSpace(run.text.String()))
	case services.PetAIEventCompleted:
		finalText := strings.TrimSpace(event.Text)
		if finalText == "" {
			finalText = strings.TrimSpace(run.text.String())
		}
		externalID := r.finishStreamingOrSend(run, finalText)
		r.saveAssistant(run, finalText, externalID)
		r.finishRun(run)
	case services.PetAIEventFailed:
		r.finishRun(run)
		r.sendFailure(run)
	case services.PetAIEventCancelled:
		r.finishRun(run)
	}
	return nil
}

func (r *AgentRuntime) updateStreaming(run *channelAgentRun, text string) {
	if !run.instance.Features.StreamingReply || text == "" {
		return
	}
	run.streamMu.Lock()
	defer run.streamMu.Unlock()
	if run.stream == nil {
		if provider, err := r.managerProvider(run.instance.ID); err == nil && provider.SupportsStreaming() {
			handle, err := provider.SendStreamingMessage(context.Background(), run.incoming.ChatID, text, run.incoming.ExternalID)
			if err == nil {
				run.stream = handle
				return
			}
			r.publishError(run.instance, err)
		}
		return
	}
	if err := run.stream.Update(context.Background(), text); err != nil {
		r.publishError(run.instance, err)
	}
}

func (r *AgentRuntime) finishStreamingOrSend(run *channelAgentRun, text string) string {
	run.streamMu.Lock()
	stream := run.stream
	run.stream = nil
	run.streamMu.Unlock()
	if stream != nil {
		messageID := ""
		if identified, ok := stream.(interface{ MessageID() string }); ok {
			messageID = strings.TrimSpace(identified.MessageID())
		}
		if err := stream.Finish(context.Background(), text); err != nil {
			r.publishError(run.instance, err)
		}
		return messageID
	}
	messageID, err := r.manager.ReplyMessage(context.Background(), run.instance.ID, run.incoming.ExternalID, text)
	if err != nil {
		messageID, err = r.manager.SendMessage(context.Background(), run.instance.ID, run.incoming.ChatID, text)
	}
	if err != nil {
		r.publishError(run.instance, err)
	}
	return messageID
}

func (r *AgentRuntime) saveAssistant(run *channelAgentRun, text, externalID string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if strings.TrimSpace(externalID) == "" {
		// 某些卡片/relay streaming API 不返回平台消息 ID；request ID 只作为本地
		// 去重兜底，绝不冒充平台 reply ID 参与下一次 ReplyMessage。
		externalID = run.requestID
	}
	message := ChannelMessage{
		ID: instanceMessageID(run.requestID), InstanceID: run.instance.ID, SessionID: run.session.ID,
		ExternalID: externalID, Role: "assistant", ChatID: run.incoming.ChatID, Content: text, Timestamp: nowMillis(),
	}
	inserted, err := r.store.AppendMessageIfNew(message)
	if err != nil {
		r.publishError(run.instance, err)
		return
	}
	if inserted {
		if session := run.session; session.ID != "" {
			session.UpdatedAt = nowMillis()
			_ = r.store.UpsertSession(session)
		}
		r.publish(ChannelEvent{Type: "message", InstanceID: run.instance.ID, PluginType: run.instance.Type, Data: message, At: nowMillis()})
	}
}

func (r *AgentRuntime) finishRun(run *channelAgentRun) {
	if run == nil {
		return
	}
	r.mu.Lock()
	if run.closed {
		r.mu.Unlock()
		return
	}
	run.closed = true
	delete(r.runs, run.requestID)
	r.mu.Unlock()
	if run.chatLock != nil {
		run.chatLock.Unlock()
	}
}

func (r *AgentRuntime) sendFailure(run *channelAgentRun) {
	if run == nil {
		return
	}
	r.sendFailureMessage(run.instance, run.incoming.ChatID)
}

func (r *AgentRuntime) sendFailureMessage(instance ChannelInstance, chatID string) {
	if r == nil || r.manager == nil {
		return
	}
	_, err := r.manager.SendMessage(context.Background(), instance.ID, chatID, "处理消息失败，请检查客户端默认模型配置和 Relay 连接。")
	if err != nil {
		r.publishError(instance, err)
	}
}

func (r *AgentRuntime) managerProvider(id string) (ChannelProvider, error) {
	return r.manager.provider(id)
}

func (r *AgentRuntime) publishError(instance ChannelInstance, err error) {
	if err == nil {
		return
	}
	r.publish(ChannelEvent{Type: "error", InstanceID: instance.ID, PluginType: instance.Type, Data: err.Error(), At: nowMillis()})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func instanceMessageID(requestID string) string {
	return "channel-message-" + strings.TrimPrefix(requestID, "channel-")
}
