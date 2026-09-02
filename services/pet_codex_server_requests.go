package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	petCodexRPCInvalidParams  = -32602
	petCodexRPCMethodNotFound = -32601
	petCodexRPCInternalError  = -32603
)

// handleCodexServerRequest 是宠物 Codex 实例的 server-request owner。
// 它不弹第二套配置面板：宠物继承 Codex 已解析的默认配置，工具审批按自主宠物
// 运行时直接接受；没有可交互 UI 或动态工具注册时则明确拒绝，避免上游一直等。
func (r *PetCodexRuntime) handleCodexServerRequest(ctx context.Context, message CodexAppServerMessage) CodexAppServerServerRequestResponse {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return petCodexServerRequestError(petCodexRPCInternalError, "pet Codex runtime is shutting down")
		default:
		}
	}

	switch strings.TrimSpace(message.Method) {
	case "item/commandExecution/requestApproval":
		return petCodexServerRequestResult(map[string]any{"decision": "accept"})
	case "item/fileChange/requestApproval":
		return petCodexServerRequestResult(map[string]any{"decision": "accept"})
	case "item/permissions/requestApproval":
		// 空权限 profile 表示不额外扩大根目录边界；scope=session 让 Codex
		// 不会为同一轮的重复检查再次挂起，而实际工作目录仍由 thread cwd 决定。
		return petCodexServerRequestResult(map[string]any{
			"permissions": map[string]any{},
			"scope":       "session",
		})
	case "mcpServer/elicitation/request":
		// 桌宠没有可安全收集第三方 MCP 表单的对话框；decline 是协议级终态，
		// 比伪造必填字段或让 MCP server 等待更符合 fail-fast 约束。
		return petCodexServerRequestResult(map[string]any{"action": "decline"})
	case "item/tool/requestUserInput":
		return petCodexServerRequestResult(petCodexEmptyUserInputAnswers(message.Params))
	case "item/tool/call":
		return r.handlePetCodexDynamicToolCall(ctx, message)
	default:
		return petCodexServerRequestError(petCodexRPCMethodNotFound, "server request is not supported by pet runtime")
	}
}

type petCodexDynamicToolCallParams struct {
	ThreadID string          `json:"threadId"`
	TurnID   string          `json:"turnId"`
	CallID   string          `json:"callId"`
	Tool     string          `json:"tool"`
	Name     string          `json:"name"`
	ToolName string          `json:"toolName"`
	Args     json.RawMessage `json:"arguments"`
}

func (r *PetCodexRuntime) handlePetCodexDynamicToolCall(ctx context.Context, message CodexAppServerMessage) CodexAppServerServerRequestResponse {
	if r == nil || r.dynamicTools == nil {
		// 没有注册 dynamicTools 时，保留协议上的 method-not-found 语义；
		// 这也是桌宠 runtime 拒绝未知 server request 的兼容行为。
		return petCodexServerRequestError(petCodexRPCMethodNotFound, "pet Codex runtime has no dynamic tools")
	}
	var params petCodexDynamicToolCallParams
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return petCodexServerRequestError(petCodexRPCInvalidParams, "dynamic tool call parameters are invalid")
	}
	threadID := strings.TrimSpace(params.ThreadID)
	turnID := strings.TrimSpace(params.TurnID)
	callID := strings.TrimSpace(params.CallID)
	toolName := firstNonEmptyPetAIString(params.Tool, params.Name, params.ToolName)
	if threadID == "" || turnID == "" || callID == "" || toolName == "" {
		return petCodexServerRequestError(petCodexRPCInvalidParams, "dynamic tool call is missing thread, turn, call id or tool")
	}
	arguments, err := normalizePetCodexToolArguments(params.Args)
	if err != nil {
		return petCodexServerRequestError(petCodexRPCInvalidParams, err.Error())
	}

	active, workspace, scope, err := r.reservePetCodexToolCall(threadID, turnID, callID, PetAgentToolName(toolName))
	if err != nil {
		return petCodexServerRequestError(petCodexRPCInvalidParams, err.Error())
	}
	defer r.releasePetCodexToolCall(active, callID)

	call := PetAgentToolCall{
		ID:        callID,
		Name:      PetAgentToolName(toolName),
		Arguments: arguments,
	}
	result := PetAgentToolResult{ToolCallID: call.ID, ToolName: string(call.Name)}
	executor, err := r.dynamicTools.NewExecutor(ctx, scope, workspace)
	if err != nil {
		result = toolExecutionFailure(result, err.Error())
		return petCodexServerRequestResult(petCodexDynamicToolResult(result))
	}
	result, err = executor.Execute(ctx, call)
	if err != nil {
		result = toolExecutionFailure(result, err.Error())
	}
	return petCodexServerRequestResult(petCodexDynamicToolResult(result))
}

func (r *PetCodexRuntime) reservePetCodexToolCall(threadID, turnID, callID string, toolName PetAgentToolName) (*petCodexActiveTurn, string, string, error) {
	if r == nil {
		return nil, "", "", errors.New("pet Codex runtime is unavailable")
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
		if active == nil || state.threadID != threadID || active.turnID != "" && active.turnID != turnID {
			state.mu.Unlock()
			continue
		}
		scope := strings.TrimSpace(active.request.ToolExecutionScope)
		if scope == "" {
			// 动态工具定义可以按项目共享，但真正执行必须绑定到当前入口的
			// 实例/session/chat；管家入口没有这个 scope 时不能猜测权限，必须
			// 让 Codex 立刻收到明确失败，而不是把请求挂在无主权限上。
			state.mu.Unlock()
			return nil, "", "", errors.New("dynamic tool execution scope is unavailable")
		}
		if _, stale := active.staleTurnIDs[turnID]; stale {
			state.mu.Unlock()
			return nil, "", "", errors.New("dynamic tool call belongs to a stale turn")
		}
		if _, registered := state.toolNames[toolName]; !registered {
			state.mu.Unlock()
			return nil, "", "", fmt.Errorf("dynamic tool %q is not registered for this thread", toolName)
		}
		if active.toolCalls == nil {
			active.toolCalls = make(map[string]struct{})
		}
		if _, inFlight := active.toolCalls[callID]; inFlight {
			state.mu.Unlock()
			return nil, "", "", fmt.Errorf("dynamic tool call %q is already in flight", callID)
		}
		active.toolCalls[callID] = struct{}{}
		if active.turnID == "" {
			// 某些 app-server 版本先发工具请求、后返回 turn/start；先记住
			// turnId，后续响应只能绑定到这一轮，不能被其它 turn 借用。
			active.turnID = turnID
		}
		workspace := state.workspace
		// ToolScope 决定 thread 使用的项目级工具快照；真正执行时必须使用
		// 当前入口的 ToolExecutionScope，避免 manager 或另一个频道借用目标权限。
		state.mu.Unlock()
		return active, workspace, scope, nil
	}
	return nil, "", "", errors.New("active Codex turn was not found for dynamic tool call")
}

func (r *PetCodexRuntime) releasePetCodexToolCall(active *petCodexActiveTurn, callID string) {
	if active == nil || active.state == nil {
		return
	}
	active.state.mu.Lock()
	if active.toolCalls != nil {
		delete(active.toolCalls, callID)
	}
	active.state.mu.Unlock()
}

func normalizePetCodexToolArguments(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`), nil
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		encoded = strings.TrimSpace(encoded)
		if encoded == "" {
			return json.RawMessage(`{}`), nil
		}
		if !json.Valid([]byte(encoded)) {
			return nil, errors.New("dynamic tool arguments are not valid JSON")
		}
		return json.RawMessage(encoded), nil
	}
	if !json.Valid(raw) {
		return nil, errors.New("dynamic tool arguments are not valid JSON")
	}
	return append(json.RawMessage(nil), raw...), nil
}

func toolExecutionFailure(result PetAgentToolResult, message string) PetAgentToolResult {
	result.IsError = true
	result.Content = strings.TrimSpace(message)
	if result.Content == "" {
		result.Content = "dynamic tool execution failed"
	}
	return result
}

func petCodexDynamicToolResult(result PetAgentToolResult) map[string]any {
	return map[string]any{
		"contentItems": []map[string]any{{
			"type": "inputText",
			"text": result.Content,
		}},
		"success": !result.IsError,
	}
}

func petCodexServerRequestResult(result any) CodexAppServerServerRequestResponse {
	return CodexAppServerServerRequestResponse{Result: result}
}

func petCodexServerRequestError(code int, message string) CodexAppServerServerRequestResponse {
	return CodexAppServerServerRequestResponse{
		Error: &CodexAppServerRPCError{Code: code, Message: message},
	}
}

func petCodexEmptyUserInputAnswers(raw json.RawMessage) map[string]any {
	var params struct {
		Questions []struct {
			ID string `json:"id"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return map[string]any{"answers": map[string]any{}}
	}
	answers := make(map[string]any, len(params.Questions))
	for _, question := range params.Questions {
		id := strings.TrimSpace(question.ID)
		if id == "" {
			continue
		}
		answers[id] = map[string]any{"answers": []string{}}
	}
	return map[string]any{"answers": answers}
}
