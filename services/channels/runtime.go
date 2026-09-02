package channels

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"codeswitch/services"
	"github.com/google/uuid"
)

// ProjectWorkspaceResolver 是频道项目绑定到当前项目事实源的窄接口。
// runtime 不直接读取 ProjectManagerService，避免把项目扫描、缓存和频道会话耦合到一起。
type ProjectWorkspaceResolver func(context.Context, string) (string, error)

// AgentRuntimeOptions 是频道专用 Codex runtime 的进程配置。模型、认证和审批
// 不在这里配置，Codex app-server 会直接继承当前客户端默认配置。
type AgentRuntimeOptions struct {
	Executable        string
	CommandFactory    services.CodexAppServerCommandFactory
	ResponseTimeout   time.Duration
	ChatRuntime       services.PetChatRuntime
	SharedChatRuntime bool
}

// AgentRuntime 把 provider 入站消息接到宿主注入的项目 Agent Hub。
// 频道不创建第二个 Codex runtime；入口权限仍通过当前频道的 execution scope
// 隔离，而 projectId 决定与 Agent 管家共用的 Codex thread。
type AgentRuntime struct {
	store           *Store
	manager         *Manager
	projectResolve  ProjectWorkspaceResolver
	chatRuntime     services.PetChatRuntime
	ownsChatRuntime bool

	mu        sync.Mutex
	runs      map[string]*channelAgentRun
	chatLocks map[string]*sync.Mutex
	// Hook 通知可能由状态轮询并发派发；发送前的幂等检查必须和平台发送、
	// 本地落库处于同一个临界区，否则重复 Hook 会在数据库去重前先发出两条平台消息。
	hookDeliveryMu sync.Mutex
	eventSink      EventSink
}

type channelAgentRun struct {
	requestID     string
	instance      ChannelInstance
	session       ChannelSession
	incoming      ChannelMessage
	textMu        sync.Mutex
	text          strings.Builder
	completedText string
	completed     bool

	streamMu sync.Mutex
	stream   StreamingHandle
	closed   bool
	chatLock *sync.Mutex
}

// NewAgentRuntime 创建频道入口适配器。Codex runtime 必须由宿主注入共享 Hub；
// 这里不再兜底创建独立 PetCodexRuntime，否则频道和 Agent 管家会重新分裂。
func NewAgentRuntime(
	store *Store,
	manager *Manager,
	projectResolve ProjectWorkspaceResolver,
	eventSink EventSink,
	options ...AgentRuntimeOptions,
) *AgentRuntime {
	var runtimeOptions AgentRuntimeOptions
	if len(options) > 0 {
		runtimeOptions = options[0]
	}
	runtime := &AgentRuntime{
		store:          store,
		manager:        manager,
		projectResolve: projectResolve,
		runs:           make(map[string]*channelAgentRun),
		chatLocks:      make(map[string]*sync.Mutex),
		eventSink:      eventSink,
	}
	runtime.chatRuntime = runtimeOptions.ChatRuntime
	runtime.ownsChatRuntime = runtime.chatRuntime != nil && !runtimeOptions.SharedChatRuntime
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

// Close 只关闭频道 runtime 自己创建的 Codex app-server；不会按进程名扫描或
// 终止用户已有的 codex 进程，应用退出时由 main 负责在频道 store 前调用它。
func (r *AgentRuntime) Close() error {
	if r == nil || r.chatRuntime == nil || !r.ownsChatRuntime {
		return nil
	}
	return r.chatRuntime.Close()
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
	if err != nil || !found || !instance.Enabled || !instance.Features.AutoReply {
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
	localImages, err := r.persistIncomingMedia(message)
	if err != nil {
		// 消息正文已经入库且已通知 UI；媒体落盘失败时停止当前 turn，
		// 避免模型收到一个看似成功但实际缺图的请求，同时保留用户消息供重试。
		r.publishError(instance, err)
		r.sendFailureMessage(instance, message.ChatID, string(services.PET_AI_INVALID_REQUEST))
		return
	}

	requestID := "channel-" + uuid.NewString()
	run := &channelAgentRun{requestID: requestID, instance: instance, session: session, incoming: message, chatLock: chatLock}
	r.mu.Lock()
	r.runs[requestID] = run
	r.mu.Unlock()
	keepLock = true

	if r.chatRuntime == nil {
		r.finishRun(run)
		r.publishError(instance, errors.New("channel Codex runtime is unavailable"))
		r.sendFailureMessage(instance, message.ChatID, string(services.PET_AI_DEPENDENCY_UNAVAILABLE))
		return
	}
	_, err = r.chatRuntime.StartChat(context.Background(), services.PetChatRequest{
		// PetID 只代表统一的桌宠 Agent 外观；projectId 才决定 Codex
		// thread owner，不能再把频道 session ID 当成一只“伪宠物”。
		PetID:     services.DefaultPetID,
		ProjectID: *instance.ProjectID,
		RequestID: requestID,
		// Persona 由 services.AgentConversationHub 从宠物设置统一解析；频道只能
		// 提供消息与当前执行 scope，不能再通过配置创建第二个 Agent 人格 owner。
		Persona:            "",
		UserText:           message.Content,
		LocalImages:        localImages,
		ToolScope:          services.PetCodexProjectToolScope(*instance.ProjectID),
		ToolExecutionScope: channelToolScope(instance.ID, session.ID, message.ChatID),
		Source:             services.AgentConversationSourceChannel,
		ChannelInstanceID:  instance.ID,
		ChannelChatID:      message.ChatID,
	})
	if err != nil {
		r.finishRun(run)
		r.publishError(instance, err)
		r.sendFailureMessage(instance, message.ChatID, channelPetAIErrorCode(err))
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

func (r *AgentRuntime) persistIncomingMedia(message ChannelMessage) ([]services.PetAILocalImage, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("channel media store is unavailable")
	}
	localImages := make([]services.PetAILocalImage, 0, len(message.Images))
	for _, media := range message.Images {
		if err := r.store.SaveMedia(message.ID, message.InstanceID, media); err != nil {
			return nil, fmt.Errorf("save channel image: %w", err)
		}
		path, err := r.store.MaterializeImage(message.ID, message.InstanceID, media)
		if err != nil {
			return nil, fmt.Errorf("materialize channel image: %w", err)
		}
		localImages = append(localImages, services.PetAILocalImage{
			Path: path,
			// 落盘校验允许带参数的 MIME，但 Codex/历史解析只需要规范化的
			// image/* 类型；统一使用同一规范，避免 content-type 参数让视觉附件
			// 在入站、turn/start 和历史回显之间产生不一致。
			MediaType: normalizeChannelImageMediaType(media.MediaType),
		})
	}
	if message.Audio != nil {
		if err := r.store.SaveMedia(message.ID, message.InstanceID, *message.Audio); err != nil {
			return nil, fmt.Errorf("save channel audio: %w", err)
		}
	}
	return localImages, nil
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
		run.textMu.Lock()
		run.text.WriteString(event.Delta)
		text := strings.TrimSpace(run.text.String())
		run.textMu.Unlock()
		r.updateStreaming(run, text)
	case services.PetAIEventCompleted:
		run.textMu.Lock()
		finalText := strings.TrimSpace(event.Text)
		if finalText == "" {
			finalText = strings.TrimSpace(run.text.String())
		}
		run.completedText = finalText
		run.completed = true
		run.textMu.Unlock()
		// 最终投递由 Hub 的 BroadcastProject 统一触发；这里保留 run 和聊天锁，
		// 让 broadcaster 能把原频道作为一个目标完成 streaming，而不是重复发送。
	case services.PetAIEventFailed:
		errorCode := ""
		if event.Error != nil {
			errorCode = event.Error.Code
		}
		r.finishRun(run)
		r.sendFailure(run, errorCode)
	case services.PetAIEventCancelled:
		r.finishRun(run)
	}
	return nil
}

// BroadcastProject 是 Hub 的频道投递边界。原请求所在频道只完成一次原地
// streaming/reply；其它实例只有显式配置 broadcastChatId 才会接收项目广播。
func (r *AgentRuntime) BroadcastProject(ctx context.Context, projectID, text, requestID string) []services.AgentDeliveryResult {
	if r == nil || r.store == nil || r.manager == nil {
		return []services.AgentDeliveryResult{{ProjectID: strings.TrimSpace(projectID), Error: "channel broadcaster is unavailable"}}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	projectID = strings.TrimSpace(projectID)
	text = strings.TrimSpace(text)
	requestID = strings.TrimSpace(requestID)
	if projectID == "" || text == "" {
		return []services.AgentDeliveryResult{{ProjectID: projectID, Error: "project broadcast requires project and text"}}
	}

	results := make([]services.AgentDeliveryResult, 0)
	delivered := make(map[string]struct{})
	var original *channelAgentRun
	r.mu.Lock()
	if run := r.runs[requestID]; run != nil {
		original = run
	}
	r.mu.Unlock()
	if original != nil {
		original.textMu.Lock()
		if strings.TrimSpace(original.completedText) != "" {
			text = strings.TrimSpace(original.completedText)
		}
		original.textMu.Unlock()
		externalID := r.finishStreamingOrSend(ctx, original, text)
		r.saveAssistant(original, text, externalID)
		result := services.AgentDeliveryResult{ProjectID: projectID, InstanceID: original.instance.ID, ChatID: original.incoming.ChatID, MessageID: externalID}
		if strings.TrimSpace(externalID) == "" {
			result.Error = "original channel reply did not return a message id"
		}
		results = append(results, result)
		delivered[original.instance.ID+"\x00"+original.incoming.ChatID] = struct{}{}
		r.finishRun(original)
	}

	instances, err := r.store.ListInstances()
	if err != nil {
		return append(results, services.AgentDeliveryResult{ProjectID: projectID, Error: err.Error()})
	}
	for _, instance := range instances {
		if !instance.Enabled || instance.ProjectID == nil || strings.TrimSpace(*instance.ProjectID) != projectID {
			continue
		}
		chatID := strings.TrimSpace(instance.Config["broadcastChatId"])
		if chatID == "" {
			continue
		}
		key := instance.ID + "\x00" + chatID
		if _, exists := delivered[key]; exists {
			continue
		}
		result := services.AgentDeliveryResult{ProjectID: projectID, InstanceID: instance.ID, ChatID: chatID}
		messageID, sendErr := r.manager.SendMessage(ctx, instance.ID, chatID, text)
		if sendErr != nil {
			result.Error = sendErr.Error()
			results = append(results, result)
			continue
		}
		result.MessageID = messageID
		if persistErr := appendChannelOutboundMessage(r.store, r.eventSink, instance, chatID, text, messageID); persistErr != nil {
			result.Error = persistErr.Error()
		}
		results = append(results, result)
		delivered[key] = struct{}{}
	}
	return results
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

func (r *AgentRuntime) finishStreamingOrSend(ctx context.Context, run *channelAgentRun, text string) string {
	if ctx == nil {
		ctx = context.Background()
	}
	run.streamMu.Lock()
	stream := run.stream
	run.stream = nil
	run.streamMu.Unlock()
	if stream != nil {
		messageID := ""
		if identified, ok := stream.(interface{ MessageID() string }); ok {
			messageID = strings.TrimSpace(identified.MessageID())
		}
		if err := stream.Finish(ctx, text); err != nil {
			r.publishError(run.instance, err)
		}
		return messageID
	}
	messageID, err := r.manager.ReplyMessage(ctx, run.instance.ID, run.incoming.ExternalID, text)
	if err != nil {
		messageID, err = r.manager.SendMessage(ctx, run.instance.ID, run.incoming.ChatID, text)
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

func (r *AgentRuntime) sendFailure(run *channelAgentRun, errorCode string) {
	if run == nil {
		return
	}
	r.sendFailureMessage(run.instance, run.incoming.ChatID, errorCode)
}

func (r *AgentRuntime) sendFailureMessage(instance ChannelInstance, chatID, errorCode string) {
	if r == nil || r.manager == nil {
		return
	}
	message := channelFailureMessage(errorCode)
	if message == "" {
		return
	}
	_, err := r.manager.SendMessage(context.Background(), instance.ID, chatID, message)
	if err != nil {
		r.publishError(instance, err)
	}
}

func channelPetAIErrorCode(err error) string {
	if err == nil {
		return string(services.PET_AI_UPSTREAM_ERROR)
	}
	if code := services.PetAIErrorCodeOf(err); code != "" {
		return code
	}
	if errors.Is(err, context.Canceled) {
		return string(services.PET_AI_REQUEST_CANCELLED)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return string(services.PET_AI_TIMEOUT)
	}
	return string(services.PET_AI_UPSTREAM_ERROR)
}

func channelFailureMessage(errorCode string) string {
	switch strings.TrimSpace(errorCode) {
	case string(services.PET_AI_REQUEST_CANCELLED):
		// 取消是用户主动收口，不应再往频道里补一条看似失败的消息。
		return ""
	case string(services.PET_AI_DEPENDENCY_UNAVAILABLE):
		return "Codex CLI 当前不可用，请确认 Codex CLI 已登录且项目绑定有效；频道会继承 Codex 默认配置。"
	case string(services.PET_AI_TIMEOUT):
		return "Codex CLI 响应超时，请确认 Codex CLI 进程仍在运行后重试。"
	case string(services.PET_AI_REQUEST_IN_FLIGHT):
		return "当前项目已有消息正在处理，请稍后再试。"
	case string(services.PET_AI_QUEUE_FULL):
		return "当前项目消息队列已满，请稍后再试。"
	case string(services.PET_AI_INVALID_REQUEST), string(services.PET_AI_WORKSPACE_UNAVAILABLE):
		return "频道请求无效，请检查项目绑定和工作目录。"
	case string(services.PET_AI_EVENT_ERROR):
		return "Codex 事件通道异常，请稍后重试并查看运行日志。"
	case string(services.PET_AI_RESPONSE_INVALID):
		return "Codex CLI 返回了无效响应，请查看运行日志。"
	case string(services.PET_AI_REQUEST_TOO_LARGE), string(services.PET_AI_RESPONSE_TOO_LARGE):
		return "消息内容超过 Codex 处理限制，请缩短后重试。"
	case string(services.PET_AI_UPSTREAM_ERROR):
		return "Codex CLI 返回了上游错误，请稍后重试并查看运行日志。"
	default:
		return "Codex CLI 处理消息失败，请稍后重试并查看运行日志。"
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
