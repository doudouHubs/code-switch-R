package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type agentConversationHubTestRuntime struct {
	mu        sync.Mutex
	emitter   PetAIEventEmitter
	starts    chan PetChatRequest
	cancels   chan string
	closeCall chan struct{}
}

func newAgentConversationHubTestRuntime() *agentConversationHubTestRuntime {
	return &agentConversationHubTestRuntime{
		starts:    make(chan PetChatRequest, 16),
		cancels:   make(chan string, 16),
		closeCall: make(chan struct{}, 1),
	}
}

func (r *agentConversationHubTestRuntime) StartChat(ctx context.Context, request PetChatRequest) (PetChatStartResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	emitter := r.emitter
	r.mu.Unlock()
	if emitter != nil {
		if err := emitter.Emit(PetAIEvent{Type: PetAIEventStarted, RequestID: request.RequestID}); err != nil {
			return PetChatStartResult{}, err
		}
	}
	select {
	case r.starts <- request:
	case <-ctx.Done():
		return PetChatStartResult{}, ctx.Err()
	}
	<-ctx.Done()
	return PetChatStartResult{}, ctx.Err()
}

func (r *agentConversationHubTestRuntime) CancelChat(requestID string) error {
	r.cancels <- requestID
	return nil
}

func (r *agentConversationHubTestRuntime) Close() error {
	select {
	case r.closeCall <- struct{}{}:
	default:
	}
	return nil
}

func (r *agentConversationHubTestRuntime) complete(requestID, text string) error {
	r.mu.Lock()
	emitter := r.emitter
	r.mu.Unlock()
	return emitter.Emit(PetAIEvent{Type: PetAIEventCompleted, RequestID: requestID, Text: text})
}

func (r *agentConversationHubTestRuntime) fail(requestID string) error {
	r.mu.Lock()
	emitter := r.emitter
	r.mu.Unlock()
	return emitter.Emit(PetAIEvent{Type: PetAIEventFailed, RequestID: requestID})
}

type agentConversationHubTestEmitter struct {
	events chan PetAIEvent
}

func newAgentConversationHubTestEmitter() *agentConversationHubTestEmitter {
	return &agentConversationHubTestEmitter{events: make(chan PetAIEvent, 64)}
}

func (e *agentConversationHubTestEmitter) Emit(event PetAIEvent) error {
	e.events <- event
	return nil
}

func agentConversationTestRequest(projectID, petID, requestID string) AgentConversationRequest {
	return AgentConversationRequest{
		ProjectID: projectID,
		PetID:     petID,
		RequestID: requestID,
		UserText:  requestID,
	}
}

func waitAgentConversationStart(t *testing.T, runtime *agentConversationHubTestRuntime) PetChatRequest {
	t.Helper()
	select {
	case request := <-runtime.starts:
		return request
	case <-time.After(2 * time.Second):
		t.Fatal("等待项目 Agent 启动超时")
		return PetChatRequest{}
	}
}

func TestAgentConversationHubPassesLocalImagesWithoutSerializingTheirPaths(t *testing.T) {
	hub, runtime, _ := newAgentConversationHubTest(t, 4)
	request := agentConversationTestRequest("project-local-image", "pet-local-image", "request-local-image")
	request.LocalImages = []PetAILocalImage{{Path: `C:\channel-media\image.png`, MediaType: "image/png"}}
	if _, err := hub.StartConversation(context.Background(), request); err != nil {
		t.Fatalf("启动本地图片请求失败: %v", err)
	}
	started := waitAgentConversationStart(t, runtime)
	if len(started.LocalImages) != 1 || started.LocalImages[0] != request.LocalImages[0] {
		t.Fatalf("Hub 未透传本地图片引用: %#v", started.LocalImages)
	}
	encoded, err := json.Marshal(started)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "channel-media") || strings.Contains(string(encoded), "localImages") {
		t.Fatalf("本地图片路径进入公共请求 JSON: %s", encoded)
	}
	_ = runtime.complete(request.RequestID, "完成")
}

func waitAgentConversationEvent(t *testing.T, emitter *agentConversationHubTestEmitter, eventType PetAIEventType, requestID string) PetAIEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-emitter.events:
			if event.Type == eventType && (requestID == "" || event.RequestID == requestID) {
				return event
			}
		case <-deadline:
			t.Fatalf("等待事件超时: type=%q request_id=%q", eventType, requestID)
			return PetAIEvent{}
		}
	}
}

func waitAgentConversationEvents(t *testing.T, emitter *agentConversationHubTestEmitter, eventType PetAIEventType, requestIDs ...string) map[string]PetAIEvent {
	t.Helper()
	wanted := make(map[string]struct{}, len(requestIDs))
	for _, requestID := range requestIDs {
		wanted[requestID] = struct{}{}
	}
	result := make(map[string]PetAIEvent, len(wanted))
	deadline := time.After(2 * time.Second)
	for len(result) < len(wanted) {
		select {
		case event := <-emitter.events:
			if event.Type != eventType {
				continue
			}
			if _, ok := wanted[event.RequestID]; ok {
				result[event.RequestID] = event
			}
		case <-deadline:
			t.Fatalf("等待事件集合超时: type=%q request_ids=%v", eventType, requestIDs)
		}
	}
	return result
}

func assertNoAgentConversationStart(t *testing.T, runtime *agentConversationHubTestRuntime) {
	t.Helper()
	select {
	case request := <-runtime.starts:
		t.Fatalf("项目队列不应提前启动 request_id=%q", request.RequestID)
	case <-time.After(80 * time.Millisecond):
	}
}

func newAgentConversationHubTest(t *testing.T, maxQueued int) (*AgentConversationHub, *agentConversationHubTestRuntime, *agentConversationHubTestEmitter) {
	t.Helper()
	runtime := newAgentConversationHubTestRuntime()
	emitter := newAgentConversationHubTestEmitter()
	hub := NewAgentConversationHub(runtime, AgentConversationHubOptions{
		MaxQueued: maxQueued,
		Emitter:   emitter,
	})
	runtime.mu.Lock()
	runtime.emitter = hub
	runtime.mu.Unlock()
	t.Cleanup(func() {
		_ = hub.Close()
	})
	return hub, runtime, emitter
}

func TestAgentConversationHubSerializesRequestsByProject(t *testing.T) {
	hub, runtime, emitter := newAgentConversationHubTest(t, 4)
	first := agentConversationTestRequest("project-a", "pet-a", "request-1")
	second := agentConversationTestRequest("project-a", "pet-b", "request-2")
	otherProject := agentConversationTestRequest("project-b", "pet-c", "request-3")

	if _, err := hub.StartConversation(context.Background(), first); err != nil {
		t.Fatalf("启动第一条请求失败: %v", err)
	}
	startedFirst := waitAgentConversationStart(t, runtime)
	if startedFirst.RequestID != first.RequestID {
		t.Fatalf("第一条启动 request_id=%q, want %q", startedFirst.RequestID, first.RequestID)
	}
	if _, err := hub.StartConversation(context.Background(), second); err != nil {
		t.Fatalf("入队第二条请求失败: %v", err)
	}
	waitAgentConversationEvent(t, emitter, PetAIEventQueued, second.RequestID)

	// 不同项目拥有独立 FIFO，不能被 project-a 的运行中 turn 阻塞。
	if _, err := hub.StartConversation(context.Background(), otherProject); err != nil {
		t.Fatalf("启动另一项目请求失败: %v", err)
	}
	startedOther := waitAgentConversationStart(t, runtime)
	if startedOther.RequestID != otherProject.RequestID {
		t.Fatalf("另一项目启动 request_id=%q, want %q", startedOther.RequestID, otherProject.RequestID)
	}
	assertNoAgentConversationStart(t, runtime)

	if err := runtime.complete(first.RequestID, "first"); err != nil {
		t.Fatalf("完成第一条请求失败: %v", err)
	}
	startedSecond := waitAgentConversationStart(t, runtime)
	if startedSecond.RequestID != second.RequestID {
		t.Fatalf("同项目第二条启动 request_id=%q, want %q", startedSecond.RequestID, second.RequestID)
	}
	if err := runtime.complete(otherProject.RequestID, "other"); err != nil {
		t.Fatalf("完成另一项目请求失败: %v", err)
	}
	if err := runtime.complete(second.RequestID, "second"); err != nil {
		t.Fatalf("完成第二条请求失败: %v", err)
	}
	waitAgentConversationEvents(t, emitter, PetAIEventCompleted, first.RequestID, second.RequestID, otherProject.RequestID)
}

func TestAgentConversationHubCancelsQueuedRequestWithTerminalEvent(t *testing.T) {
	hub, runtime, emitter := newAgentConversationHubTest(t, 4)
	first := agentConversationTestRequest("project-a", "pet-a", "request-1")
	second := agentConversationTestRequest("project-a", "pet-a", "request-2")

	if _, err := hub.StartConversation(context.Background(), first); err != nil {
		t.Fatalf("启动第一条请求失败: %v", err)
	}
	waitAgentConversationStart(t, runtime)
	if _, err := hub.StartConversation(context.Background(), second); err != nil {
		t.Fatalf("入队第二条请求失败: %v", err)
	}
	waitAgentConversationEvent(t, emitter, PetAIEventQueued, second.RequestID)

	if err := hub.CancelChat(second.RequestID); err != nil {
		t.Fatalf("取消排队请求失败: %v", err)
	}
	cancelled := waitAgentConversationEvent(t, emitter, PetAIEventCancelled, second.RequestID)
	if cancelled.Sequence == 0 {
		t.Fatal("排队取消事件缺少 sequence")
	}
	assertNoAgentConversationStart(t, runtime)

	if err := runtime.complete(first.RequestID, "first"); err != nil {
		t.Fatalf("完成第一条请求失败: %v", err)
	}
	waitAgentConversationEvent(t, emitter, PetAIEventCompleted, first.RequestID)
}

func TestAgentConversationHubRejectsDuplicateAndFullQueue(t *testing.T) {
	hub, runtime, emitter := newAgentConversationHubTest(t, 1)
	first := agentConversationTestRequest("project-a", "pet-a", "request-1")
	second := agentConversationTestRequest("project-a", "pet-a", "request-2")
	third := agentConversationTestRequest("project-a", "pet-a", "request-3")

	if _, err := hub.StartConversation(context.Background(), first); err != nil {
		t.Fatalf("启动第一条请求失败: %v", err)
	}
	waitAgentConversationStart(t, runtime)
	if _, err := hub.StartConversation(context.Background(), second); err != nil {
		t.Fatalf("入队第二条请求失败: %v", err)
	}
	waitAgentConversationEvent(t, emitter, PetAIEventQueued, second.RequestID)

	if _, err := hub.StartConversation(context.Background(), agentConversationTestRequest("project-b", "pet-b", first.RequestID)); err == nil || PetAIErrorCodeOf(err) != string(PET_AI_REQUEST_IN_FLIGHT) {
		t.Fatalf("重复 request id 错误=%q, want %q", PetAIErrorCodeOf(err), PET_AI_REQUEST_IN_FLIGHT)
	}
	if _, err := hub.StartConversation(context.Background(), third); err == nil || PetAIErrorCodeOf(err) != string(PET_AI_QUEUE_FULL) {
		t.Fatalf("队列满错误=%q, want %q", PetAIErrorCodeOf(err), PET_AI_QUEUE_FULL)
	}

	if err := runtime.complete(first.RequestID, "first"); err != nil {
		t.Fatalf("完成第一条请求失败: %v", err)
	}
	waitAgentConversationStart(t, runtime)
	if err := runtime.complete(second.RequestID, "second"); err != nil {
		t.Fatalf("完成第二条请求失败: %v", err)
	}
}

func TestAgentConversationHubCloseCancelsRunningAndQueuedJobs(t *testing.T) {
	hub, runtime, emitter := newAgentConversationHubTest(t, 4)
	first := agentConversationTestRequest("project-a", "pet-a", "request-1")
	second := agentConversationTestRequest("project-a", "pet-a", "request-2")

	if _, err := hub.StartConversation(context.Background(), first); err != nil {
		t.Fatalf("启动第一条请求失败: %v", err)
	}
	waitAgentConversationStart(t, runtime)
	if _, err := hub.StartConversation(context.Background(), second); err != nil {
		t.Fatalf("入队第二条请求失败: %v", err)
	}
	waitAgentConversationEvent(t, emitter, PetAIEventQueued, second.RequestID)

	if err := hub.Close(); err != nil {
		t.Fatalf("关闭 Hub 失败: %v", err)
	}
	waitAgentConversationEvents(t, emitter, PetAIEventCancelled, first.RequestID, second.RequestID)
	select {
	case <-runtime.closeCall:
	default:
		t.Fatal("关闭 Hub 未关闭底层 runtime")
	}
	// 底层 runtime 的迟到 completed 不能穿透已关闭的 Hub。
	if err := runtime.complete(first.RequestID, "late"); err != nil {
		t.Fatalf("发送迟到事件失败: %v", err)
	}
	select {
	case event := <-emitter.events:
		t.Fatalf("关闭后仍收到事件 type=%q request_id=%q", event.Type, event.RequestID)
	case <-time.After(80 * time.Millisecond):
	}
}

func TestAgentConversationHubUsesCanonicalPersona(t *testing.T) {
	runtime := newAgentConversationHubTestRuntime()
	emitter := newAgentConversationHubTestEmitter()
	var resolvedProjectID, resolvedPetID string
	hub := NewAgentConversationHub(runtime, AgentConversationHubOptions{
		Emitter: emitter,
		PersonaResolver: AgentConversationPersonaResolverFunc(func(_ context.Context, projectID, petID string) (string, error) {
			resolvedProjectID = projectID
			resolvedPetID = petID
			return "canonical persona", nil
		}),
	})
	runtime.mu.Lock()
	runtime.emitter = hub
	runtime.mu.Unlock()
	defer hub.Close()

	request := agentConversationTestRequest("project-canonical", "pet-canonical", "request-canonical")
	request.Persona = "caller supplied persona"
	if _, err := hub.StartConversation(context.Background(), request); err != nil {
		t.Fatalf("启动 canonical persona 请求失败: %v", err)
	}
	started := waitAgentConversationStart(t, runtime)
	if started.Persona != "canonical persona" {
		t.Fatalf("runtime persona = %q, want canonical persona", started.Persona)
	}
	if resolvedProjectID != request.ProjectID || resolvedPetID != request.PetID {
		t.Fatalf("persona resolver args = project:%q pet:%q", resolvedProjectID, resolvedPetID)
	}
	if err := runtime.complete(request.RequestID, "done"); err != nil {
		t.Fatalf("完成 canonical persona 请求失败: %v", err)
	}
	waitAgentConversationEvent(t, emitter, PetAIEventCompleted, request.RequestID)
}

func TestAgentConversationHubReportsPersonaFailureAfterAcceptingRequest(t *testing.T) {
	runtime := newAgentConversationHubTestRuntime()
	emitter := newAgentConversationHubTestEmitter()
	hub := NewAgentConversationHub(runtime, AgentConversationHubOptions{
		Emitter: emitter,
		PersonaResolver: AgentConversationPersonaResolverFunc(func(context.Context, string, string) (string, error) {
			return "", errors.New("persona storage is temporarily unavailable")
		}),
	})
	runtime.mu.Lock()
	runtime.emitter = hub
	runtime.mu.Unlock()
	defer hub.Close()

	request := agentConversationTestRequest("project-persona-failure", "pet-persona-failure", "request-persona-failure")
	result, err := hub.StartConversation(context.Background(), request)
	if err != nil {
		t.Fatalf("人格解析失败不应阻止请求入队: %v", err)
	}
	if result.RequestID != request.RequestID {
		t.Fatalf("accepted request id = %q, want %q", result.RequestID, request.RequestID)
	}
	failed := waitAgentConversationEvent(t, emitter, PetAIEventFailed, request.RequestID)
	if failed.Error == nil || failed.Error.Code != string(PET_AI_DEPENDENCY_UNAVAILABLE) {
		t.Fatalf("persona failure event = %#v, want %s", failed.Error, PET_AI_DEPENDENCY_UNAVAILABLE)
	}
	assertNoAgentConversationStart(t, runtime)
}
