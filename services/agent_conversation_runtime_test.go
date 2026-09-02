package services

import (
	"context"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

type agentCodexSessionMemory struct {
	mu       sync.Mutex
	sessions map[string]AgentCodexSession
}

func (m *agentCodexSessionMemory) LoadAgentCodexSession(_ context.Context, projectID string) (*AgentCodexSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[projectID]
	if !ok {
		return nil, nil
	}
	copy := session
	return &copy, nil
}

func (m *agentCodexSessionMemory) SaveAgentCodexSession(_ context.Context, session AgentCodexSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions == nil {
		m.sessions = make(map[string]AgentCodexSession)
	}
	m.sessions[session.ProjectID] = session
	return nil
}

func TestPetCodexRuntimeSharesStateAndThreadAcrossAgentEntriesByProject(t *testing.T) {
	workspace := filepath.Clean(t.TempDir())
	const projectID = "project-shared-agent"
	const canonicalPersona = "canonical Agent persona"
	var processCount atomic.Int32
	recorder := &petCodexEventRecorder{}
	sessions := &agentCodexSessionMemory{sessions: make(map[string]AgentCodexSession)}
	var hub *AgentConversationHub
	factory := func(executable string, args ...string) *exec.Cmd {
		processCount.Add(1)
		return newCodexFixtureFactory("pet-complete", func() string { return "shared-project-thread" })(executable, args...)
	}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		AgentSessions: sessions,
		ProjectWorkspaceResolver: ProjectWorkspaceResolverFunc(func(_ context.Context, id string) (string, error) {
			if id != projectID {
				t.Fatalf("project resolver id = %q, want %q", id, projectID)
			}
			return workspace, nil
		}),
		Emitter: PetAIEventEmitterFunc(func(event PetAIEvent) error {
			if hub == nil {
				return nil
			}
			return hub.Emit(event)
		}),
		CommandFactory: factory,
	})
	hub = NewAgentConversationHub(runtime, AgentConversationHubOptions{
		Emitter: recorder,
		PersonaResolver: AgentConversationPersonaResolverFunc(func(_ context.Context, _, _ string) (string, error) {
			return canonicalPersona, nil
		}),
	})
	defer hub.Close()

	managerRequest := AgentConversationRequest{
		ProjectID: projectID,
		PetID:     "manager-entry-pet",
		RequestID: "manager-entry-request",
		Persona:   "manager supplied persona",
		UserText:  "来自 Agent 管家的消息",
	}
	if _, err := hub.StartConversation(context.Background(), managerRequest); err != nil {
		t.Fatalf("启动 Agent 管家请求失败: %v", err)
	}
	if completed := recorder.waitFor(managerRequest.RequestID, PetAIEventCompleted); completed.Text == "" {
		t.Fatal("Agent 管家请求没有收到完成文本")
	}

	channelRequest := AgentConversationRequest{
		ProjectID:         projectID,
		PetID:             "channel-entry-pet",
		RequestID:         "channel-entry-request",
		Source:            AgentConversationSourceChannel,
		ChannelInstanceID: "channel-instance",
		ChannelChatID:     "channel-chat",
		Persona:           "channel supplied persona",
		UserText:          "来自聊天频道的消息",
	}
	if _, err := hub.StartConversation(context.Background(), channelRequest); err != nil {
		t.Fatalf("启动频道请求失败: %v", err)
	}
	if completed := recorder.waitFor(channelRequest.RequestID, PetAIEventCompleted); completed.Text == "" {
		t.Fatal("频道请求没有收到完成文本")
	}

	managerState := runtime.stateForConversation(projectID, managerRequest.PetID)
	channelState := runtime.stateForConversation(projectID, channelRequest.PetID)
	if managerState != channelState {
		t.Fatal("同一 projectId 的 Agent 管家和频道没有复用同一个 runtime state")
	}
	managerState.mu.Lock()
	threadID := managerState.threadID
	managerState.mu.Unlock()
	if threadID != "shared-project-thread" {
		t.Fatalf("共享 project thread = %q, want shared-project-thread", threadID)
	}
	if processCount.Load() != 1 {
		t.Fatalf("Codex app-server process count = %d, want 1", processCount.Load())
	}
	session, err := sessions.LoadAgentCodexSession(context.Background(), projectID)
	if err != nil || session == nil || session.ThreadID != threadID {
		t.Fatalf("共享 project session = %#v, err=%v", session, err)
	}
}

func TestPetCodexRuntimeMigratesLegacyPetSessionToAgentSession(t *testing.T) {
	workspace := filepath.Clean(t.TempDir())
	const projectID = "project-legacy-session"
	const petID = "legacy-session-pet"
	const persona = "legacy persona"
	dao := NewPetDAO(newPetMigrationTestDB(t))
	if err := dao.SaveCodexSession(context.Background(), PetCodexSession{
		PetID:              petID,
		ThreadID:           "legacy-thread",
		Workspace:          workspace,
		PersonaFingerprint: petCodexPersonaFingerprint(persona),
		ProtocolVersion:    PetCodexPlanProtocolVersion,
		UpdatedAt:          100,
	}); err != nil {
		t.Fatalf("保存旧 pet session 失败: %v", err)
	}
	recorder := &petCodexEventRecorder{}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions:      dao,
		AgentSessions: dao,
		ProjectWorkspaceResolver: ProjectWorkspaceResolverFunc(func(_ context.Context, id string) (string, error) {
			if id != projectID {
				t.Fatalf("project resolver id = %q, want %q", id, projectID)
			}
			return workspace, nil
		}),
		Emitter:        recorder,
		CommandFactory: newCodexFixtureFactory("pet-complete", func() string { return "legacy-thread" }),
	})
	defer runtime.Close()

	request := PetChatRequest{
		ProjectID: projectID,
		PetID:     petID,
		RequestID: "legacy-session-request",
		Persona:   persona,
		UserText:  "迁移旧会话",
	}
	if _, err := runtime.StartChat(context.Background(), request); err != nil {
		t.Fatalf("启动旧会话请求失败: %v", err)
	}
	if completed := recorder.waitFor(request.RequestID, PetAIEventCompleted); completed.Text == "" {
		t.Fatal("旧会话请求没有收到完成文本")
	}
	agentSession, err := dao.LoadAgentCodexSession(context.Background(), projectID)
	if err != nil {
		t.Fatalf("读取迁移后的 agent session 失败: %v", err)
	}
	if agentSession == nil || agentSession.ThreadID != "legacy-thread" || agentSession.Workspace != workspace || agentSession.PersonaFingerprint != petCodexPersonaFingerprint(persona) {
		t.Fatalf("迁移后的 agent session = %#v", agentSession)
	}
}
