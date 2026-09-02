package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

const (
	petCodexMaxSkillReferences       = 16
	petCodexMaxInteractionQuestions  = 16
	petCodexMaxInteractionOptions    = 16
	petCodexMaxInteractionTextLength = 8 << 10
	petCodexMaxInteractionSchemaSize = 64 << 10
	petCodexMaxInteractionAnswerSize = 8 << 10
	petCodexMaxModelListLimit        = 1000
)

// PetCodexCommandRuntime 是 Agent 管家控制面的窄接口。普通聊天仍通过
// PetChatRuntime 进入；这些方法只负责调用 Codex app-server 原生控制协议，
// 不复制 provider、模型或权限配置。
type PetCodexCommandRuntime interface {
	ListSkills(context.Context, AgentCommandRequest) (AgentSkillListResult, error)
	ListModels(context.Context, AgentCommandRequest) (AgentModelListResult, error)
	ExecuteCommand(context.Context, AgentCommandRequest) (AgentCommandResult, error)
	ResolveInteraction(context.Context, ResolveInteractionRequest) error
}

type petCodexPendingInteraction struct {
	client          *CodexAppServerClient
	serverRequestID json.RawMessage
	state           *petCodexPetState
	interaction     PetAIInteraction
}

type petCodexServerRequestFields struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

type petCodexCommandApprovalParams struct {
	ApprovalID         string   `json:"approvalId"`
	AvailableDecisions []string `json:"availableDecisions"`
	Command            string   `json:"command"`
	CWD                string   `json:"cwd"`
	ItemID             string   `json:"itemId"`
	Reason             string   `json:"reason"`
	ThreadID           string   `json:"threadId"`
	TurnID             string   `json:"turnId"`
}

type petCodexFileApprovalParams struct {
	GrantRoot string `json:"grantRoot"`
	ItemID    string `json:"itemId"`
	Reason    string `json:"reason"`
	ThreadID  string `json:"threadId"`
	TurnID    string `json:"turnId"`
}

type petCodexPermissionApprovalParams struct {
	CWD         string          `json:"cwd"`
	ItemID      string          `json:"itemId"`
	Permissions json.RawMessage `json:"permissions"`
	Reason      string          `json:"reason"`
	ThreadID    string          `json:"threadId"`
	TurnID      string          `json:"turnId"`
}

type petCodexUserInputParams struct {
	ItemID    string                      `json:"itemId"`
	Questions []petCodexUserInputQuestion `json:"questions"`
	ThreadID  string                      `json:"threadId"`
	TurnID    string                      `json:"turnId"`
}

type petCodexUserInputQuestion struct {
	Header   string                    `json:"header"`
	ID       string                    `json:"id"`
	IsOther  bool                      `json:"isOther"`
	IsSecret bool                      `json:"isSecret"`
	Options  []petCodexUserInputOption `json:"options"`
	Question string                    `json:"question"`
}

type petCodexUserInputOption struct {
	Description string `json:"description"`
	Label       string `json:"label"`
}

type petCodexMCPFormParams struct {
	Message         string          `json:"message"`
	Mode            string          `json:"mode"`
	RequestedSchema json.RawMessage `json:"requestedSchema"`
	ServerName      string          `json:"serverName"`
	ThreadID        string          `json:"threadId"`
	TurnID          string          `json:"turnId"`
}

type petCodexRawSkillsResponse struct {
	Data []struct {
		CWD    string `json:"cwd"`
		Errors []struct {
			Message string `json:"message"`
			Path    string `json:"path"`
		} `json:"errors"`
		Skills []struct {
			Description      string `json:"description"`
			Enabled          bool   `json:"enabled"`
			Name             string `json:"name"`
			Path             string `json:"path"`
			Scope            string `json:"scope"`
			ShortDescription string `json:"shortDescription"`
		} `json:"skills"`
	} `json:"data"`
}

type petCodexRawModelsResponse struct {
	Data []struct {
		DefaultReasoningEffort string   `json:"defaultReasoningEffort"`
		Description            string   `json:"description"`
		DisplayName            string   `json:"displayName"`
		Hidden                 bool     `json:"hidden"`
		ID                     string   `json:"id"`
		InputModalities        []string `json:"inputModalities"`
		IsDefault              bool     `json:"isDefault"`
		Model                  string   `json:"model"`
	} `json:"data"`
	NextCursor *string `json:"nextCursor"`
}

func (r *PetCodexRuntime) ListSkills(ctx context.Context, request AgentCommandRequest) (AgentSkillListResult, error) {
	request.Command = "skills"
	normalized, err := normalizeAgentCommandRequest(request)
	if err != nil {
		return AgentSkillListResult{}, err
	}
	_, client, workspace, _, _, err := r.commandClient(ctx, normalized)
	if err != nil {
		return AgentSkillListResult{}, err
	}
	response, err := client.Call(ctx, "skills/list", map[string]any{
		"cwds":        []string{workspace},
		"forceReload": normalized.ForceReload,
	})
	if err != nil {
		return AgentSkillListResult{}, newPetAIError(petCodexStartErrorCode(err), 0, err)
	}
	result, err := decodePetCodexSkillsResponse(response, normalized.ProjectID, workspace)
	if err != nil {
		return AgentSkillListResult{}, newPetAIError(PET_AI_RESPONSE_INVALID, 0, err)
	}
	return result, nil
}

func (r *PetCodexRuntime) ListModels(ctx context.Context, request AgentCommandRequest) (AgentModelListResult, error) {
	request.Command = "models"
	normalized, err := normalizeAgentCommandRequest(request)
	if err != nil {
		return AgentModelListResult{}, err
	}
	_, client, workspace, _, _, err := r.commandClient(ctx, normalized)
	if err != nil {
		return AgentModelListResult{}, err
	}
	params := map[string]any{
		"cursor":        nil,
		"includeHidden": normalized.IncludeHidden,
	}
	if cursor := strings.TrimSpace(normalized.Cursor); cursor != "" {
		params["cursor"] = cursor
	}
	if normalized.Limit > 0 {
		params["limit"] = normalized.Limit
	}
	response, err := client.Call(ctx, "model/list", params)
	if err != nil {
		return AgentModelListResult{}, newPetAIError(petCodexStartErrorCode(err), 0, err)
	}
	result, err := decodePetCodexModelsResponse(response, normalized.ProjectID, workspace)
	if err != nil {
		return AgentModelListResult{}, newPetAIError(PET_AI_RESPONSE_INVALID, 0, err)
	}
	return result, nil
}

func (r *PetCodexRuntime) ExecuteCommand(ctx context.Context, request AgentCommandRequest) (AgentCommandResult, error) {
	normalized, err := normalizeAgentCommandRequest(request)
	if err != nil {
		return AgentCommandResult{}, err
	}
	switch normalized.Command {
	case "skills":
		result, err := r.ListSkills(ctx, normalized)
		if err != nil {
			return AgentCommandResult{}, err
		}
		return AgentCommandResult{Command: normalized.Command, Accepted: true, Skills: result.Skills}, nil
	case "models":
		result, err := r.ListModels(ctx, normalized)
		if err != nil {
			return AgentCommandResult{}, err
		}
		return AgentCommandResult{Command: normalized.Command, Accepted: true, Models: result.Models, Raw: mustMarshalAgentCommandResult(result)}, nil
	case "review":
		return r.startReviewCommand(ctx, normalized)
	case "compact":
		return r.executeCompactCommand(ctx, normalized)
	case "steer":
		return r.executeSteerCommand(ctx, normalized)
	case "interrupt":
		return r.executeInterruptCommand(ctx, normalized)
	default:
		return AgentCommandResult{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, errors.New("unsupported Agent command"))
	}
}

func (r *PetCodexRuntime) commandClient(ctx context.Context, request AgentCommandRequest) (*petCodexPetState, *CodexAppServerClient, string, string, *petCodexActiveTurn, error) {
	if r == nil || !r.hasWorkspaceResolver(request.ProjectID) || !r.hasSessionRepository(request.ProjectID) {
		return nil, nil, "", "", nil, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	modelReference, err := r.loadPetAgentModelReference(ctx, request.PetID)
	if err != nil {
		return nil, nil, "", "", nil, err
	}
	workspace, err := r.resolveConversationWorkspace(ctx, request.ProjectID, request.PetID)
	if err != nil {
		return nil, nil, "", "", nil, err
	}
	toolScope := ""
	if request.ProjectID != "" {
		toolScope = PetCodexProjectToolScope(request.ProjectID)
	}
	toolSnapshot, err := r.snapshotDynamicTools(toolScope)
	if err != nil {
		return nil, nil, "", "", nil, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, err)
	}
	state := r.stateForConversation(request.ProjectID, request.PetID)
	state.startMu.Lock()
	client, _, err := r.ensureSession(ctx, state, workspace, request.ProjectID, request.PetID, request.Persona, modelReference, toolScope, toolSnapshot)
	state.startMu.Unlock()
	if err != nil {
		return nil, nil, "", "", nil, err
	}
	state.mu.Lock()
	threadID := strings.TrimSpace(state.threadID)
	active := state.active
	state.mu.Unlock()
	if client == nil || threadID == "" {
		return nil, nil, "", "", nil, newPetAIError(PET_AI_RESPONSE_INVALID, 0, errors.New("Codex thread is unavailable"))
	}
	r.registerCodexHookSource(petCodexChatInput{
		ProjectID:   request.ProjectID,
		ProjectName: request.ProjectName,
		Source:      request.Source,
		SessionName: request.SessionName,
		PetID:       request.PetID,
	}, workspace, threadID, "")
	return state, client, workspace, threadID, active, nil
}

func (r *PetCodexRuntime) executeCompactCommand(ctx context.Context, request AgentCommandRequest) (AgentCommandResult, error) {
	_, client, _, threadID, active, err := r.commandClient(ctx, request)
	if err != nil {
		return AgentCommandResult{}, err
	}
	if active != nil {
		return AgentCommandResult{}, newPetAIError(PET_AI_REQUEST_IN_FLIGHT, 0, nil)
	}
	response, err := client.Call(ctx, "thread/compact/start", map[string]any{"threadId": threadID})
	if err != nil {
		return AgentCommandResult{}, newPetAIError(petCodexStartErrorCode(err), 0, err)
	}
	return AgentCommandResult{
		Command:  request.Command,
		Accepted: true,
		ThreadID: threadID,
		Raw:      append(json.RawMessage(nil), response...),
	}, nil
}

func (r *PetCodexRuntime) executeSteerCommand(ctx context.Context, request AgentCommandRequest) (AgentCommandResult, error) {
	state, client, _, threadID, active, err := r.commandClient(ctx, request)
	if err != nil {
		return AgentCommandResult{}, err
	}
	if active == nil {
		return AgentCommandResult{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, errors.New("there is no active Codex turn to steer"))
	}
	state.mu.Lock()
	expectedTurnID := strings.TrimSpace(request.ExpectedTurnID)
	if expectedTurnID == "" {
		expectedTurnID = strings.TrimSpace(active.turnID)
	}
	state.mu.Unlock()
	if expectedTurnID == "" || strings.TrimSpace(request.Input) == "" {
		return AgentCommandResult{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	params := map[string]any{
		"threadId":       threadID,
		"expectedTurnId": expectedTurnID,
		"input":          []map[string]any{{"type": "text", "text": request.Input}},
	}
	if request.RequestID != "" {
		params["clientUserMessageId"] = request.RequestID
	}
	response, err := client.Call(ctx, "turn/steer", params)
	if err != nil {
		return AgentCommandResult{}, newPetAIError(petCodexStartErrorCode(err), 0, err)
	}
	var result struct {
		TurnID string `json:"turnId"`
	}
	if err := json.Unmarshal(response, &result); err != nil || strings.TrimSpace(result.TurnID) == "" {
		return AgentCommandResult{}, newPetAIError(PET_AI_RESPONSE_INVALID, 0, nil)
	}
	state.mu.Lock()
	if state.active == active {
		active.turnID = strings.TrimSpace(result.TurnID)
	}
	state.mu.Unlock()
	return AgentCommandResult{
		Command:  request.Command,
		Accepted: true,
		ThreadID: threadID,
		TurnID:   strings.TrimSpace(result.TurnID),
		Raw:      append(json.RawMessage(nil), response...),
	}, nil
}

func (r *PetCodexRuntime) executeInterruptCommand(ctx context.Context, request AgentCommandRequest) (AgentCommandResult, error) {
	state, client, _, threadID, active, err := r.commandClient(ctx, request)
	if err != nil {
		return AgentCommandResult{}, err
	}
	if active == nil {
		return AgentCommandResult{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, errors.New("there is no active Codex turn to interrupt"))
	}
	state.mu.Lock()
	turnID := strings.TrimSpace(active.turnID)
	if turnID == "" {
		if active.startCancel != nil {
			active.cancelled = true
			cancel := active.startCancel
			state.mu.Unlock()
			cancel()
			return AgentCommandResult{Command: request.Command, Accepted: true, ThreadID: threadID}, nil
		}
		state.mu.Unlock()
		return AgentCommandResult{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, errors.New("active Codex turn has no turn id"))
	}
	if active.interruptSent {
		state.mu.Unlock()
		return AgentCommandResult{Command: request.Command, Accepted: true, ThreadID: threadID, TurnID: turnID}, nil
	}
	active.interruptSent = true
	state.mu.Unlock()
	response, err := client.Call(ctx, "turn/interrupt", map[string]any{
		"threadId": threadID,
		"turnId":   turnID,
	})
	if err != nil {
		return AgentCommandResult{}, newPetAIError(petCodexStartErrorCode(err), 0, err)
	}
	return AgentCommandResult{
		Command:  request.Command,
		Accepted: true,
		ThreadID: threadID,
		TurnID:   turnID,
		Raw:      append(json.RawMessage(nil), response...),
	}, nil
}

func (r *PetCodexRuntime) startReviewCommand(ctx context.Context, request AgentCommandRequest) (AgentCommandResult, error) {
	if request.Delivery == "" {
		request.Delivery = "inline"
	}
	if request.Delivery != "inline" {
		// detached review 会产生第二条 thread；当前产品的 Agent 管家与频道
		// 明确共享一个项目会话，因此不能悄悄把它变成脱离主会话的副本。
		return AgentCommandResult{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, errors.New("detached review is not supported for the shared Agent thread"))
	}
	target, err := parsePetCodexReviewTarget(request.Args)
	if err != nil {
		return AgentCommandResult{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, err)
	}
	if request.RequestID == "" {
		request.RequestID = newPetCodexCommandRequestID()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	modelReference, err := r.loadPetAgentModelReference(ctx, request.PetID)
	if err != nil {
		return AgentCommandResult{}, err
	}
	workspace, err := r.resolveConversationWorkspace(ctx, request.ProjectID, request.PetID)
	if err != nil {
		return AgentCommandResult{}, err
	}
	toolScope := ""
	if request.ProjectID != "" {
		toolScope = PetCodexProjectToolScope(request.ProjectID)
	}
	toolSnapshot, err := r.snapshotDynamicTools(toolScope)
	if err != nil {
		return AgentCommandResult{}, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, err)
	}
	state := r.stateForConversation(request.ProjectID, request.PetID)
	state.mu.Lock()
	hasActive := state.active != nil
	state.mu.Unlock()
	if hasActive {
		return AgentCommandResult{}, newPetAIError(PET_AI_REQUEST_IN_FLIGHT, 0, nil)
	}
	startCtx, startCancel := context.WithCancel(ctx)
	active := &petCodexActiveTurn{
		state:          state,
		modelReference: modelReference,
		startCancel:    startCancel,
		request: petCodexChatInput{
			PetID:              request.PetID,
			ProjectID:          request.ProjectID,
			ProjectName:        request.ProjectName,
			RequestID:          request.RequestID,
			Source:             request.Source,
			SessionName:        request.SessionName,
			Persona:            request.Persona,
			ToolScope:          toolScope,
			ToolFingerprint:    toolSnapshot.Fingerprint,
			ToolDefinitions:    toolSnapshot.Definitions,
			ToolExecutionScope: "",
		},
		toolCalls: make(map[string]struct{}),
	}
	if !r.registerActive(active, nil) {
		startCancel()
		return AgentCommandResult{}, newPetAIError(PET_AI_REQUEST_IN_FLIGHT, 0, nil)
	}
	if err := r.emitActiveEvent(active, PetAIEvent{Type: PetAIEventStarted}); err != nil {
		failure := r.releaseActiveForStartFailure(active)
		r.removeRequest(request.RequestID, active)
		if failure.startCancel != nil {
			failure.startCancel()
		}
		return AgentCommandResult{}, err
	}
	go r.startReviewTurn(startCtx, active, workspace, target)
	state.mu.Lock()
	threadID := strings.TrimSpace(state.threadID)
	state.mu.Unlock()
	return AgentCommandResult{
		Command:   request.Command,
		Accepted:  true,
		RequestID: request.RequestID,
		ThreadID:  threadID,
	}, nil
}

func (r *PetCodexRuntime) startReviewTurn(ctx context.Context, active *petCodexActiveTurn, workspace string, target map[string]any) {
	state := active.state
	state.startMu.Lock()
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
	if err == nil {
		owned := false
		threadID := ""
		state.mu.Lock()
		owned = state.active == active
		threadID = strings.TrimSpace(state.threadID)
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
		// 来源登记只读取已复制到局部变量的 threadID；同时放到 startMu
		// 临界区外，避免状态服务的登记锁与 session 启动锁形成反向等待。
		state.startMu.Unlock()
		if !owned {
			return
		}
		r.registerCodexHookSource(active.request, workspace, threadID, "")
	} else {
		state.startMu.Unlock()
	}
	if err != nil {
		r.finishStartFailure(active, petCodexStartErrorCode(err), nil)
		return
	}
	state.mu.Lock()
	cancelled := active.cancelled
	state.mu.Unlock()
	if cancelled || ctx.Err() != nil {
		r.finishStartFailure(active, PET_AI_REQUEST_CANCELLED, nil)
		return
	}
	response, err := client.Call(ctx, "review/start", map[string]any{
		"threadId": active.state.threadID,
		"target":   target,
		"delivery": "inline",
	})
	if err != nil {
		r.finishStartFailure(active, petCodexStartErrorCode(err), client)
		return
	}
	var result struct {
		ReviewThreadID string `json:"reviewThreadId"`
		Turn           struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(response, &result); err != nil || strings.TrimSpace(result.ReviewThreadID) == "" || strings.TrimSpace(result.Turn.ID) == "" {
		r.finishStartFailure(active, PET_AI_RESPONSE_INVALID, client)
		return
	}
	state.mu.Lock()
	owned := state.active == active
	if owned {
		if strings.TrimSpace(result.ReviewThreadID) != strings.TrimSpace(state.threadID) {
			owned = false
		}
		if owned {
			active.turnID = strings.TrimSpace(result.Turn.ID)
			cancelled = active.cancelled
		}
	}
	state.mu.Unlock()
	if !owned {
		return
	}
	r.registerCodexHookSource(active.request, workspace, active.state.threadID, active.turnID)
	if cancelled {
		r.interruptTurn(active)
	}
}

func parsePetCodexReviewTarget(args []string) (map[string]any, error) {
	if len(args) == 0 {
		return map[string]any{"type": "uncommittedChanges"}, nil
	}
	targetType := strings.ToLower(strings.TrimSpace(args[0]))
	switch targetType {
	case "uncommitted", "uncommittedchanges", "changes":
		if len(args) != 1 {
			return nil, errors.New("uncommitted review does not accept extra arguments")
		}
		return map[string]any{"type": "uncommittedChanges"}, nil
	case "base", "basebranch":
		if len(args) != 2 || strings.TrimSpace(args[1]) == "" {
			return nil, errors.New("base branch review requires a branch")
		}
		return map[string]any{"type": "baseBranch", "branch": strings.TrimSpace(args[1])}, nil
	case "commit":
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
			return nil, errors.New("commit review requires a commit sha")
		}
		target := map[string]any{"type": "commit", "sha": strings.TrimSpace(args[1])}
		if len(args) > 2 {
			target["title"] = strings.TrimSpace(strings.Join(args[2:], " "))
		}
		return target, nil
	case "custom":
		instructions := strings.TrimSpace(strings.Join(args[1:], " "))
		if instructions == "" {
			return nil, errors.New("custom review requires instructions")
		}
		if runeLen(instructions) > PetAIMaxUserTextLength || hasLineBreak(instructions) {
			return nil, errors.New("custom review instructions are invalid")
		}
		return map[string]any{"type": "custom", "instructions": instructions}, nil
	default:
		return nil, fmt.Errorf("unsupported review target %q", args[0])
	}
}

func normalizeAgentCommandRequest(request AgentCommandRequest) (AgentCommandRequest, error) {
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.ProjectName = strings.TrimSpace(request.ProjectName)
	request.PetID = strings.TrimSpace(request.PetID)
	if request.PetID == "" {
		request.PetID = DefaultPetID
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.Source = strings.ToLower(strings.TrimSpace(request.Source))
	if request.Source == "" {
		request.Source = AgentConversationSourceManager
	}
	request.Persona = strings.TrimSpace(request.Persona)
	request.SessionName = strings.TrimSpace(request.SessionName)
	request.Command = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(request.Command), "/"))
	request.Cursor = strings.TrimSpace(request.Cursor)
	request.Delivery = strings.ToLower(strings.TrimSpace(request.Delivery))
	request.ExpectedTurnID = strings.TrimSpace(request.ExpectedTurnID)
	request.Input = strings.TrimSpace(request.Input)
	if request.Command == "skill" {
		request.Command = "skills"
	}
	if request.Command == "model" {
		request.Command = "models"
	}
	if request.Command == "compact/start" {
		request.Command = "compact"
	}
	if request.Command == "turn/steer" {
		request.Command = "steer"
	}
	if request.Command == "turn/interrupt" {
		request.Command = "interrupt"
	}
	if request.Command == "" || (request.ProjectID == "" && request.PetID == "") {
		return AgentCommandRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if runeLen(request.ProjectID) > PetAIMaxProjectFolderLength || hasLineBreak(request.ProjectID) || strings.IndexByte(request.ProjectID, 0) >= 0 {
		return AgentCommandRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if runeLen(request.PetID) > PetAIMaxPetIDLength || hasLineBreak(request.PetID) {
		return AgentCommandRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if request.RequestID != "" && (runeLen(request.RequestID) > PetAIMaxRequestIDLength || hasLineBreak(request.RequestID)) {
		return AgentCommandRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if runeLen(request.Persona) > PetAIMaxPersonaLength || runeLen(request.ProjectName) > PetAIMaxRequestIDLength || runeLen(request.SessionName) > PetAIMaxRequestIDLength || runeLen(request.Cursor) > PetAIMaxTotalInputLength || runeLen(request.ExpectedTurnID) > PetAIMaxRequestIDLength || runeLen(request.Input) > PetAIMaxUserTextLength || hasLineBreak(request.Input) || hasLineBreak(request.Cursor) || hasLineBreak(request.ExpectedTurnID) || hasLineBreak(request.ProjectName) || hasLineBreak(request.SessionName) {
		return AgentCommandRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if request.Limit < 0 || request.Limit > petCodexMaxModelListLimit {
		return AgentCommandRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	for index, arg := range request.Args {
		arg = strings.TrimSpace(arg)
		if arg == "" || runeLen(arg) > PetAIMaxUserTextLength || hasLineBreak(arg) || strings.IndexByte(arg, 0) >= 0 {
			return AgentCommandRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
		}
		request.Args[index] = arg
	}
	if request.Source != "" && request.Source != AgentConversationSourceManager && request.Source != AgentConversationSourceChannel {
		return AgentCommandRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	return request, nil
}

func newPetCodexCommandRequestID() string {
	return fmt.Sprintf("agent-command-%d", time.Now().UnixNano())
}

func mustMarshalAgentCommandResult(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return encoded
}

func decodePetCodexSkillsResponse(raw json.RawMessage, projectID, workspace string) (AgentSkillListResult, error) {
	var response petCodexRawSkillsResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return AgentSkillListResult{}, err
	}
	result := AgentSkillListResult{
		ProjectID: projectID,
		Workspace: workspace,
		Skills:    make([]AgentSkill, 0),
		Errors:    make([]AgentSkillError, 0),
	}
	for _, entry := range response.Data {
		if strings.TrimSpace(entry.CWD) != "" && !samePetCodexPath(entry.CWD, workspace) {
			continue
		}
		for _, skill := range entry.Skills {
			name := strings.TrimSpace(skill.Name)
			path := strings.TrimSpace(skill.Path)
			if name == "" || path == "" {
				continue
			}
			result.Skills = append(result.Skills, AgentSkill{
				Name:             boundedPetCodexInteractionText(name),
				Description:      boundedPetCodexInteractionText(skill.Description),
				ShortDescription: boundedPetCodexInteractionText(skill.ShortDescription),
				Path:             filepath.Clean(path),
				Scope:            boundedPetCodexInteractionText(skill.Scope),
				Enabled:          skill.Enabled,
			})
		}
		for _, skillError := range entry.Errors {
			result.Errors = append(result.Errors, AgentSkillError{
				Message: boundedPetCodexInteractionText(skillError.Message),
				Path:    boundedPetCodexInteractionText(skillError.Path),
			})
		}
	}
	return result, nil
}

func decodePetCodexModelsResponse(raw json.RawMessage, projectID, workspace string) (AgentModelListResult, error) {
	var response petCodexRawModelsResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return AgentModelListResult{}, err
	}
	result := AgentModelListResult{ProjectID: projectID, Workspace: workspace, Models: make([]AgentModel, 0)}
	for _, model := range response.Data {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		displayName := strings.TrimSpace(model.DisplayName)
		if displayName == "" {
			displayName = firstNonEmptyPetAIString(model.Model, id)
		}
		result.Models = append(result.Models, AgentModel{
			ID:                     id,
			Model:                  strings.TrimSpace(model.Model),
			DisplayName:            boundedPetCodexInteractionText(displayName),
			Description:            boundedPetCodexInteractionText(model.Description),
			Hidden:                 model.Hidden,
			IsDefault:              model.IsDefault,
			InputModalities:        append([]string(nil), model.InputModalities...),
			DefaultReasoningEffort: strings.TrimSpace(model.DefaultReasoningEffort),
		})
	}
	if response.NextCursor != nil {
		result.NextCursor = strings.TrimSpace(*response.NextCursor)
	}
	return result, nil
}

func normalizePetCodexSkillReferences(skills []AgentSkillReference) ([]AgentSkillReference, error) {
	if len(skills) > petCodexMaxSkillReferences {
		return nil, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	result := make([]AgentSkillReference, 0, len(skills))
	seen := make(map[string]struct{}, len(skills))
	for _, skill := range skills {
		name := strings.TrimSpace(skill.Name)
		path := strings.TrimSpace(skill.Path)
		if name == "" || path == "" || runeLen(name) > PetAIMaxModelLength || runeLen(path) > PetAIMaxProjectFolderLength || hasLineBreak(name) || hasLineBreak(path) || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) {
			return nil, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
		}
		path = filepath.Clean(path)
		key := name + "\x00" + path
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, AgentSkillReference{Name: name, Path: path})
	}
	return result, nil
}

func (r *PetCodexRuntime) validatePetCodexSkills(ctx context.Context, client *CodexAppServerClient, workspace string, selected []AgentSkillReference) error {
	if len(selected) == 0 {
		return nil
	}
	response, err := client.Call(ctx, "skills/list", map[string]any{
		"cwds":        []string{workspace},
		"forceReload": false,
	})
	if err != nil {
		return err
	}
	available, err := decodePetCodexSkillsResponse(response, "", workspace)
	if err != nil {
		return err
	}
	for _, requested := range selected {
		found := false
		for _, skill := range available.Skills {
			if skill.Enabled && skill.Name == requested.Name && samePetCodexPath(skill.Path, requested.Path) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("selected Codex skill %q is no longer available", requested.Name)
		}
	}
	return nil
}

func samePetCodexPath(left, right string) bool {
	left = filepath.Clean(strings.TrimSpace(left))
	right = filepath.Clean(strings.TrimSpace(right))
	if left == "" || right == "" {
		return false
	}
	if abs, err := filepath.Abs(left); err == nil {
		left = filepath.Clean(abs)
	}
	if abs, err := filepath.Abs(right); err == nil {
		right = filepath.Clean(abs)
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func isPetCodexInteractiveServerRequest(method string) bool {
	switch strings.TrimSpace(method) {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "mcpServer/elicitation/request", "item/tool/requestUserInput":
		return true
	default:
		return false
	}
}

func (r *PetCodexRuntime) observeCodexServerRequest(_ context.Context, message CodexAppServerMessage) bool {
	if !isPetCodexInteractiveServerRequest(message.Method) || r == nil || r.isClosed() {
		return false
	}
	active := r.activeForServerRequest(message.Params)
	if active == nil || active.client == nil {
		return false
	}
	interaction, err := buildPetCodexInteraction(message)
	if err != nil {
		_ = active.client.ResolveServerRequest(message.ID, petCodexServerRequestError(petCodexRPCInvalidParams, err.Error()))
		return true
	}
	interaction.ID = r.nextPetCodexInteractionID()
	pending := &petCodexPendingInteraction{
		client:          active.client,
		serverRequestID: append(json.RawMessage(nil), message.ID...),
		state:           active.state,
		interaction:     interaction,
	}
	r.interactionMu.Lock()
	if r.closed {
		r.interactionMu.Unlock()
		return false
	}
	r.interactions[interaction.ID] = pending
	r.interactionMu.Unlock()
	if err := r.emitActiveEvent(active, PetAIEvent{
		Type:        PetAIEventInteraction,
		Interaction: &pending.interaction,
	}); err != nil {
		r.interactionMu.Lock()
		delete(r.interactions, interaction.ID)
		r.interactionMu.Unlock()
		_ = pending.client.ResolveServerRequest(pending.serverRequestID, petCodexServerRequestError(petCodexRPCInternalError, "Agent interaction could not be published"))
	}
	return true
}

func (r *PetCodexRuntime) activeForServerRequest(raw json.RawMessage) *petCodexActiveTurn {
	var fields petCodexServerRequestFields
	if json.Unmarshal(raw, &fields) != nil || strings.TrimSpace(fields.ThreadID) == "" {
		return nil
	}
	r.mu.Lock()
	states := make([]*petCodexPetState, 0, len(r.states))
	for _, state := range r.states {
		states = append(states, state)
	}
	r.mu.Unlock()
	for _, state := range states {
		state.mu.Lock()
		active := state.active
		if active != nil && state.client != nil && state.threadID == strings.TrimSpace(fields.ThreadID) {
			turnMatches := strings.TrimSpace(fields.TurnID) == "" || active.turnID == "" || active.turnID == strings.TrimSpace(fields.TurnID)
			if turnMatches {
				state.mu.Unlock()
				return active
			}
		}
		state.mu.Unlock()
	}
	return nil
}

func (r *PetCodexRuntime) nextPetCodexInteractionID() string {
	sequence := atomic.AddUint64(&r.interactionSequence, 1)
	return fmt.Sprintf("pet-interaction-%d-%d", time.Now().UnixNano(), sequence)
}

func (r *PetCodexRuntime) ResolveInteraction(ctx context.Context, request ResolveInteractionRequest) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	interactionID := strings.TrimSpace(request.InteractionID)
	if interactionID == "" || runeLen(interactionID) > PetAIMaxRequestIDLength || hasLineBreak(interactionID) {
		return newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	r.interactionMu.Lock()
	pending := r.interactions[interactionID]
	if pending == nil {
		r.interactionMu.Unlock()
		return newPetAIError(PET_AI_INVALID_REQUEST, 0, errors.New("interaction is no longer pending"))
	}
	response, err := buildPetCodexInteractionResponse(pending.interaction, request)
	if err != nil {
		r.interactionMu.Unlock()
		return newPetAIError(PET_AI_INVALID_REQUEST, 0, err)
	}
	delete(r.interactions, interactionID)
	r.interactionMu.Unlock()
	if err := pending.client.ResolveServerRequest(pending.serverRequestID, response); err != nil {
		return newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, err)
	}
	return nil
}

func (r *PetCodexRuntime) closePendingInteractions() {
	if r == nil {
		return
	}
	r.interactionMu.Lock()
	pending := make([]*petCodexPendingInteraction, 0, len(r.interactions))
	for key, interaction := range r.interactions {
		delete(r.interactions, key)
		pending = append(pending, interaction)
	}
	r.interactionMu.Unlock()
	for _, interaction := range pending {
		if interaction != nil && interaction.client != nil {
			_ = interaction.client.ResolveServerRequest(interaction.serverRequestID, petCodexServerRequestError(petCodexRPCInternalError, "Agent runtime is shutting down"))
		}
	}
}

func buildPetCodexInteraction(message CodexAppServerMessage) (PetAIInteraction, error) {
	interaction := PetAIInteraction{Method: strings.TrimSpace(message.Method)}
	switch interaction.Method {
	case "item/commandExecution/requestApproval":
		var params petCodexCommandApprovalParams
		if err := json.Unmarshal(message.Params, &params); err != nil || strings.TrimSpace(params.ItemID) == "" || strings.TrimSpace(params.ThreadID) == "" || strings.TrimSpace(params.TurnID) == "" {
			return PetAIInteraction{}, errors.New("command approval parameters are invalid")
		}
		interaction.Kind = PetAIInteractionApproval
		interaction.Title = "需要批准命令"
		interaction.ItemID = strings.TrimSpace(params.ItemID)
		interaction.CallID = strings.TrimSpace(params.ApprovalID)
		interaction.ThreadID = strings.TrimSpace(params.ThreadID)
		interaction.TurnID = strings.TrimSpace(params.TurnID)
		interaction.Command = boundedPetCodexInteractionText(params.Command)
		interaction.CWD = boundedPetCodexInteractionText(params.CWD)
		interaction.Reason = boundedPetCodexInteractionText(params.Reason)
		interaction.AvailableDecisions = normalizePetCodexDecisions(params.AvailableDecisions, []string{"accept", "acceptForSession", "decline", "cancel"})
	case "item/fileChange/requestApproval":
		var params petCodexFileApprovalParams
		if err := json.Unmarshal(message.Params, &params); err != nil || strings.TrimSpace(params.ItemID) == "" || strings.TrimSpace(params.ThreadID) == "" || strings.TrimSpace(params.TurnID) == "" {
			return PetAIInteraction{}, errors.New("file approval parameters are invalid")
		}
		interaction.Kind = PetAIInteractionApproval
		interaction.Title = "需要批准文件变更"
		interaction.ItemID = strings.TrimSpace(params.ItemID)
		interaction.ThreadID = strings.TrimSpace(params.ThreadID)
		interaction.TurnID = strings.TrimSpace(params.TurnID)
		interaction.Reason = boundedPetCodexInteractionText(params.Reason)
		interaction.CWD = boundedPetCodexInteractionText(params.GrantRoot)
		interaction.AvailableDecisions = []string{"accept", "acceptForSession", "decline", "cancel"}
	case "item/permissions/requestApproval":
		var params petCodexPermissionApprovalParams
		if err := json.Unmarshal(message.Params, &params); err != nil || strings.TrimSpace(params.ItemID) == "" || strings.TrimSpace(params.ThreadID) == "" || strings.TrimSpace(params.TurnID) == "" {
			return PetAIInteraction{}, errors.New("permission approval parameters are invalid")
		}
		interaction.Kind = PetAIInteractionPermission
		interaction.Title = "需要扩展权限"
		interaction.ItemID = strings.TrimSpace(params.ItemID)
		interaction.ThreadID = strings.TrimSpace(params.ThreadID)
		interaction.TurnID = strings.TrimSpace(params.TurnID)
		interaction.CWD = boundedPetCodexInteractionText(params.CWD)
		interaction.Reason = boundedPetCodexInteractionText(params.Reason)
		interaction.AvailableDecisions = []string{"accept", "decline", "cancel"}
		raw, err := clonePetCodexJSONMap(params.Permissions, petCodexMaxInteractionSchemaSize)
		if err != nil {
			return PetAIInteraction{}, err
		}
		if raw != nil {
			interaction.RawSchema = raw
		}
	case "item/tool/requestUserInput":
		var params petCodexUserInputParams
		if err := json.Unmarshal(message.Params, &params); err != nil || strings.TrimSpace(params.ItemID) == "" || strings.TrimSpace(params.ThreadID) == "" || strings.TrimSpace(params.TurnID) == "" {
			return PetAIInteraction{}, errors.New("user input parameters are invalid")
		}
		if len(params.Questions) > petCodexMaxInteractionQuestions {
			return PetAIInteraction{}, errors.New("too many user input questions")
		}
		interaction.Kind = PetAIInteractionUserInput
		interaction.Title = "Codex 需要补充信息"
		interaction.ItemID = strings.TrimSpace(params.ItemID)
		interaction.ThreadID = strings.TrimSpace(params.ThreadID)
		interaction.TurnID = strings.TrimSpace(params.TurnID)
		interaction.Questions = make([]PetAIInteractionQuestion, 0, len(params.Questions))
		seen := make(map[string]struct{}, len(params.Questions))
		for _, question := range params.Questions {
			id := strings.TrimSpace(question.ID)
			if id == "" || runeLen(id) > PetAIMaxRequestIDLength {
				return PetAIInteraction{}, errors.New("user input question id is invalid")
			}
			if _, exists := seen[id]; exists {
				return PetAIInteraction{}, errors.New("user input question id is duplicated")
			}
			seen[id] = struct{}{}
			if len(question.Options) > petCodexMaxInteractionOptions {
				return PetAIInteraction{}, errors.New("too many user input options")
			}
			view := PetAIInteractionQuestion{
				ID:       id,
				Header:   boundedPetCodexInteractionText(question.Header),
				Question: boundedPetCodexInteractionText(question.Question),
				Secret:   question.IsSecret,
				Other:    question.IsOther,
				Options:  make([]PetAIInteractionOption, 0, len(question.Options)),
			}
			if view.Question == "" {
				return PetAIInteraction{}, errors.New("user input question is empty")
			}
			for _, option := range question.Options {
				view.Options = append(view.Options, PetAIInteractionOption{
					Label:       boundedPetCodexInteractionText(option.Label),
					Description: boundedPetCodexInteractionText(option.Description),
				})
			}
			interaction.Questions = append(interaction.Questions, view)
		}
	case "mcpServer/elicitation/request":
		var params petCodexMCPFormParams
		if err := json.Unmarshal(message.Params, &params); err != nil || strings.TrimSpace(params.ServerName) == "" || strings.TrimSpace(params.ThreadID) == "" {
			return PetAIInteraction{}, errors.New("MCP elicitation parameters are invalid")
		}
		interaction.Kind = PetAIInteractionMCPForm
		interaction.Title = "MCP 服务需要输入"
		interaction.ServerName = boundedPetCodexInteractionText(params.ServerName)
		interaction.Message = boundedPetCodexInteractionText(params.Message)
		interaction.ThreadID = strings.TrimSpace(params.ThreadID)
		interaction.TurnID = strings.TrimSpace(params.TurnID)
		raw, err := clonePetCodexJSONMap(params.RequestedSchema, petCodexMaxInteractionSchemaSize)
		if err != nil {
			return PetAIInteraction{}, err
		}
		if raw != nil {
			interaction.RawSchema = raw
		}
	default:
		return PetAIInteraction{}, errors.New("unsupported interactive server request")
	}
	return interaction, nil
}

func buildPetCodexInteractionResponse(interaction PetAIInteraction, request ResolveInteractionRequest) (CodexAppServerServerRequestResponse, error) {
	decision := strings.TrimSpace(request.Decision)
	action := strings.TrimSpace(request.Action)
	switch interaction.Kind {
	case PetAIInteractionApproval:
		if decision == "" {
			decision = action
		}
		if !petCodexContainsString([]string{"accept", "acceptForSession", "decline", "cancel"}, decision) {
			return CodexAppServerServerRequestResponse{}, errors.New("approval decision is invalid")
		}
		return petCodexServerRequestResult(map[string]any{"decision": decision}), nil
	case PetAIInteractionPermission:
		if decision == "" {
			decision = action
		}
		if !petCodexContainsString([]string{"accept", "decline", "cancel"}, decision) {
			return CodexAppServerServerRequestResponse{}, errors.New("permission decision is invalid")
		}
		scope := strings.ToLower(strings.TrimSpace(request.Scope))
		if scope == "" {
			scope = "turn"
		}
		if scope != "turn" && scope != "session" {
			return CodexAppServerServerRequestResponse{}, errors.New("permission scope is invalid")
		}
		permissions := map[string]any{}
		if decision == "accept" {
			var err error
			permissions, err = normalizePetCodexPermissionProfile(request.Permissions)
			if err != nil {
				return CodexAppServerServerRequestResponse{}, err
			}
		} else {
			scope = "turn"
		}
		return petCodexServerRequestResult(map[string]any{"permissions": permissions, "scope": scope}), nil
	case PetAIInteractionUserInput:
		questionIDs := make(map[string]struct{}, len(interaction.Questions))
		for _, question := range interaction.Questions {
			questionIDs[question.ID] = struct{}{}
		}
		if len(request.Answers) > len(questionIDs) {
			return CodexAppServerServerRequestResponse{}, errors.New("user input contains an unknown question")
		}
		answers := make(map[string]any, len(request.Answers))
		for id, values := range request.Answers {
			id = strings.TrimSpace(id)
			if _, exists := questionIDs[id]; !exists || len(values) > petCodexMaxInteractionOptions {
				return CodexAppServerServerRequestResponse{}, errors.New("user input answer question id is invalid")
			}
			cloned := make([]string, len(values))
			for index, value := range values {
				value = strings.TrimSpace(value)
				if runeLen(value) > petCodexMaxInteractionAnswerSize || hasLineBreak(value) {
					return CodexAppServerServerRequestResponse{}, errors.New("user input answer is too large")
				}
				cloned[index] = value
			}
			answers[id] = map[string]any{"answers": cloned}
		}
		for id := range questionIDs {
			if _, exists := answers[id]; !exists {
				answers[id] = map[string]any{"answers": []string{}}
			}
		}
		return petCodexServerRequestResult(map[string]any{"answers": answers}), nil
	case PetAIInteractionMCPForm:
		if action == "" {
			action = decision
		}
		if !petCodexContainsString([]string{"accept", "decline", "cancel"}, action) {
			return CodexAppServerServerRequestResponse{}, errors.New("MCP elicitation action is invalid")
		}
		response := map[string]any{"action": action}
		if action == "accept" {
			content, err := clonePetCodexJSONMap(request.Content, petCodexMaxInteractionSchemaSize)
			if err != nil {
				return CodexAppServerServerRequestResponse{}, err
			}
			if content == nil {
				content = map[string]any{}
			}
			response["content"] = content
		}
		return petCodexServerRequestResult(response), nil
	default:
		return CodexAppServerServerRequestResponse{}, errors.New("interaction kind is invalid")
	}
}

func normalizePetCodexPermissionProfile(value map[string]any) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	for key := range value {
		if key != "fileSystem" && key != "network" {
			return nil, errors.New("permission profile contains an unknown field")
		}
	}
	clone, err := clonePetCodexJSONMap(value, petCodexMaxInteractionSchemaSize)
	if err != nil {
		return nil, err
	}
	if clone == nil {
		return map[string]any{}, nil
	}
	return clone, nil
}

func clonePetCodexJSONMap(value any, maxBytes int) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxBytes {
		return nil, errors.New("interaction JSON payload is invalid or too large")
	}
	var clone map[string]any
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return nil, errors.New("interaction JSON payload must be an object")
	}
	return clone, nil
}

func normalizePetCodexDecisions(values, fallback []string) []string {
	allowed := map[string]struct{}{
		"accept": {}, "acceptForSession": {}, "decline": {}, "cancel": {},
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, ok := allowed[value]; !ok {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) == 0 {
		return append([]string(nil), fallback...)
	}
	return result
}

func boundedPetCodexInteractionText(value string) string {
	value = strings.TrimSpace(value)
	if runeLen(value) <= petCodexMaxInteractionTextLength {
		return value
	}
	runes := []rune(value)
	return string(runes[:petCodexMaxInteractionTextLength])
}

func petCodexContainsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
