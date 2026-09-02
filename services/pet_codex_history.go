package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// PetChatHistoryRequest 是前端读取某只宠物历史时的最小输入。
// persona 参与 session 匹配，避免把旧角色的 thread 错接到新角色上。
type PetChatHistoryRequest struct {
	PetID     string `json:"petId"`
	ProjectID string `json:"projectId,omitempty"`
	Persona   string `json:"persona"`
}

type PetChatHistoryMessage struct {
	ID        string       `json:"id"`
	Role      string       `json:"role"`
	Content   string       `json:"content"`
	Images    []PetAIImage `json:"images,omitempty"`
	CreatedAt int64        `json:"createdAt,omitempty"`
	Status    string       `json:"status,omitempty"`
}

type PetChatHistoryResult struct {
	ThreadID string                  `json:"threadId"`
	Messages []PetChatHistoryMessage `json:"messages"`
}

type petCodexThreadReadResponse struct {
	Thread struct {
		ID    string            `json:"id"`
		CWD   string            `json:"cwd"`
		Turns []json.RawMessage `json:"turns"`
	} `json:"thread"`
	CWD string `json:"cwd"`
}

// GetChatHistory 从 Codex thread 读取历史。没有已持久化 session 时直接返回空，
// 因为打开聊天窗口不应偷偷创建新 Codex 进程或新 thread。
func (r *PetCodexRuntime) GetChatHistory(ctx context.Context, request PetChatHistoryRequest) (PetChatHistoryResult, error) {
	input, err := normalizePetChatHistoryRequest(request)
	if err != nil {
		return PetChatHistoryResult{}, err
	}
	writePetCodexDiagnostic(
		"pet-codex-history-start",
		fmt.Sprintf("pet_id=%q", input.PetID),
		fmt.Sprintf("project_id=%q", input.ProjectID),
	)
	if ctx == nil {
		ctx = context.Background()
	}
	modelReference, err := r.loadPetAgentModelReference(ctx, input.PetID)
	if err != nil {
		return PetChatHistoryResult{}, err
	}
	if r == nil || !r.hasWorkspaceResolver(input.ProjectID) || !r.hasSessionRepository(input.ProjectID) {
		writePetCodexDiagnostic(
			"pet-codex-history-dependency-error",
			fmt.Sprintf("pet_id=%q", input.PetID),
			fmt.Sprintf("error_code=%q", PET_AI_DEPENDENCY_UNAVAILABLE),
		)
		return PetChatHistoryResult{}, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}

	writePetCodexDiagnostic(
		"pet-codex-history-workspace-start",
		fmt.Sprintf("pet_id=%q", input.PetID),
	)
	workspace, err := r.resolveConversationWorkspace(ctx, input.ProjectID, input.PetID)
	if err != nil {
		writePetCodexDiagnostic(
			"pet-codex-history-workspace-error",
			fmt.Sprintf("pet_id=%q", input.PetID),
			fmt.Sprintf("error_code=%q", PetAIErrorCodeOf(err)),
		)
		return PetChatHistoryResult{}, err
	}
	writePetCodexDiagnostic(
		"pet-codex-history-workspace-ready",
		fmt.Sprintf("pet_id=%q", input.PetID),
	)
	state := r.stateForConversation(input.ProjectID, input.PetID)
	state.mu.Lock()
	active := state.active != nil
	client := state.client
	stateThreadID := strings.TrimSpace(state.threadID)
	stateWorkspace := state.workspace
	stateToolScope := state.toolScope
	stateToolFingerprint := state.toolFingerprint
	statePersonaFingerprint := state.personaFingerprint
	stateProtocolVersion := state.protocolVersion
	stateModel := strings.TrimSpace(state.model)
	state.mu.Unlock()
	if active {
		writePetCodexDiagnostic(
			"pet-codex-history-in-flight",
			fmt.Sprintf("pet_id=%q", input.PetID),
			fmt.Sprintf("error_code=%q", PET_AI_REQUEST_IN_FLIGHT),
		)
		return PetChatHistoryResult{}, newPetAIError(PET_AI_REQUEST_IN_FLIGHT, 0, nil)
	}

	// 历史读取不能把内存中的旧指纹当成当前权限快照；频道配置可能在
	// thread 存活期间改变，必须重新计算当前 scope 的工具集合后再决定能否复用。
	toolScope := strings.TrimSpace(stateToolScope)
	if toolScope == "" && input.ProjectID != "" {
		toolScope = PetCodexProjectToolScope(input.ProjectID)
	}
	toolSnapshot := PetCodexDynamicToolSnapshot{}
	currentToolFingerprint := ""
	if r.dynamicTools == nil {
		currentToolFingerprint = stateToolFingerprint
		toolSnapshot.Fingerprint = currentToolFingerprint
	} else if toolScope != "" {
		var snapshotErr error
		toolSnapshot, snapshotErr = r.snapshotDynamicTools(toolScope)
		if snapshotErr != nil {
			return PetChatHistoryResult{}, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, snapshotErr)
		}
		currentToolFingerprint = strings.TrimSpace(toolSnapshot.Fingerprint)
	}
	compatible := client != nil && stateThreadID != "" &&
		stateToolFingerprint == currentToolFingerprint &&
		samePetCodexWorkspace(stateWorkspace, workspace) &&
		statePersonaFingerprint == petCodexPersonaFingerprint(input.Persona) &&
		stateProtocolVersion == PetCodexPlanProtocolVersion &&
		(modelReference.ModelID == "" || stateModel == modelReference.ModelID)
	writePetCodexDiagnostic(
		"pet-codex-history-session-state",
		fmt.Sprintf("pet_id=%q", input.PetID),
		fmt.Sprintf("compatible=%t", compatible),
		fmt.Sprintf("has_client=%t", client != nil),
		fmt.Sprintf("thread_id=%q", stateThreadID),
	)

	if !compatible {
		// 没有已保存 thread 时返回空，不调用 ensureSession，避免单纯打开聊天
		// 面板就创建无历史的 Codex 会话。
		writePetCodexDiagnostic(
			"pet-codex-history-session-load-start",
			fmt.Sprintf("pet_id=%q", input.PetID),
		)
		loaded, loadErr := r.loadSession(ctx, input.ProjectID, input.PetID)
		if loadErr != nil {
			writePetCodexDiagnostic(
				"pet-codex-history-session-load-error",
				fmt.Sprintf("pet_id=%q", input.PetID),
				fmt.Sprintf("error_code=%q", PET_AI_DEPENDENCY_UNAVAILABLE),
			)
			return PetChatHistoryResult{}, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, loadErr)
		}
		session := loaded.session
		if session == nil || strings.TrimSpace(session.ThreadID) == "" ||
			!samePetCodexWorkspace(session.Workspace, workspace) ||
			session.PersonaFingerprint != petCodexPersonaFingerprint(input.Persona) ||
			session.ProtocolVersion != PetCodexPlanProtocolVersion ||
			strings.TrimSpace(session.ToolFingerprint) != currentToolFingerprint {
			writePetCodexDiagnostic(
				"pet-codex-history-session-empty",
				fmt.Sprintf("pet_id=%q", input.PetID),
			)
			return PetChatHistoryResult{Messages: []PetChatHistoryMessage{}}, nil
		}
		writePetCodexDiagnostic(
			"pet-codex-history-session-loaded",
			fmt.Sprintf("pet_id=%q", input.PetID),
			fmt.Sprintf("thread_id=%q", strings.TrimSpace(session.ThreadID)),
		)
	}

	// thread/read 与 session 握手共用同一把锁；这能避免历史读取和首次发送
	// 并发创建两个 app-server，或者把 read 发到刚被替换的旧 thread。
	state.startMu.Lock()
	defer state.startMu.Unlock()
	client, _, err = r.ensureSession(ctx, state, workspace, input.ProjectID, input.PetID, input.Persona, modelReference, toolScope, toolSnapshot)
	if err != nil {
		return PetChatHistoryResult{}, err
	}
	state.mu.Lock()
	threadID := strings.TrimSpace(state.threadID)
	state.mu.Unlock()
	if threadID == "" {
		writePetCodexDiagnostic(
			"pet-codex-history-thread-error",
			fmt.Sprintf("pet_id=%q", input.PetID),
			fmt.Sprintf("error_code=%q", PET_AI_RESPONSE_INVALID),
		)
		return PetChatHistoryResult{}, newPetAIError(PET_AI_RESPONSE_INVALID, 0, errors.New("Codex thread id is empty"))
	}

	writePetCodexDiagnostic(
		"pet-codex-history-thread-read-start",
		fmt.Sprintf("pet_id=%q", input.PetID),
		fmt.Sprintf("thread_id=%q", threadID),
	)
	response, err := client.Call(ctx, "thread/read", map[string]any{
		"threadId":     threadID,
		"includeTurns": true,
	})
	if err != nil {
		writePetCodexDiagnostic(
			"pet-codex-history-thread-read-error",
			fmt.Sprintf("pet_id=%q", input.PetID),
			fmt.Sprintf("thread_id=%q", threadID),
			fmt.Sprintf("error_code=%q", petCodexStartErrorCode(err)),
		)
		return PetChatHistoryResult{}, newPetAIError(petCodexStartErrorCode(err), 0, err)
	}
	writePetCodexDiagnostic(
		"pet-codex-history-response-received",
		fmt.Sprintf("pet_id=%q", input.PetID),
		fmt.Sprintf("thread_id=%q", threadID),
	)
	history, err := parsePetCodexHistoryResponse(response, workspace, threadID, r.localImageRoots)
	if err != nil {
		writePetCodexDiagnostic(
			"pet-codex-history-response-error",
			fmt.Sprintf("pet_id=%q", input.PetID),
			fmt.Sprintf("thread_id=%q", threadID),
			fmt.Sprintf("error_code=%q", PET_AI_RESPONSE_INVALID),
		)
		return PetChatHistoryResult{}, newPetAIError(PET_AI_RESPONSE_INVALID, 0, err)
	}
	writePetCodexDiagnostic(
		"pet-codex-history-success",
		fmt.Sprintf("pet_id=%q", input.PetID),
		fmt.Sprintf("thread_id=%q", threadID),
		fmt.Sprintf("message_count=%d", len(history.Messages)),
	)
	return history, nil
}

func normalizePetChatHistoryRequest(request PetChatHistoryRequest) (PetChatHistoryRequest, error) {
	request.PetID = strings.TrimSpace(request.PetID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.Persona = strings.TrimSpace(request.Persona)
	if request.PetID == "" || runeLen(request.PetID) > PetAIMaxPetIDLength || hasLineBreak(request.PetID) {
		return PetChatHistoryRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if runeLen(request.ProjectID) > PetAIMaxProjectFolderLength || hasLineBreak(request.ProjectID) || strings.IndexByte(request.ProjectID, 0) >= 0 {
		return PetChatHistoryRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if request.Persona == "" || runeLen(request.Persona) > PetAIMaxPersonaLength {
		return PetChatHistoryRequest{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	return request, nil
}

func parsePetCodexHistoryResponse(raw json.RawMessage, workspace, expectedThreadID string, localImageRoots ...[]string) (PetChatHistoryResult, error) {
	var response petCodexThreadReadResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return PetChatHistoryResult{}, err
	}
	response.Thread.ID = strings.TrimSpace(response.Thread.ID)
	response.Thread.CWD = strings.TrimSpace(response.Thread.CWD)
	response.CWD = strings.TrimSpace(response.CWD)
	threadCWD := response.Thread.CWD
	if threadCWD == "" {
		threadCWD = response.CWD
	}
	if response.Thread.ID == "" || !samePetCodexWorkspace(threadCWD, workspace) {
		return PetChatHistoryResult{}, errors.New("Codex thread/read 返回的 thread 或 cwd 不符合预期")
	}
	if response.CWD != "" && !samePetCodexWorkspace(response.CWD, workspace) {
		return PetChatHistoryResult{}, errors.New("Codex thread/read 返回了不属于宠物项目的 cwd")
	}
	if expectedThreadID != "" && response.Thread.ID != strings.TrimSpace(expectedThreadID) {
		return PetChatHistoryResult{}, errors.New("Codex thread/read 返回了不同的 thread id")
	}

	messages := make([]PetChatHistoryMessage, 0)
	for turnIndex, rawTurn := range response.Thread.Turns {
		turnMessages, err := parsePetCodexHistoryTurn(rawTurn, turnIndex, localImageRoots...)
		if err != nil {
			return PetChatHistoryResult{}, err
		}
		messages = append(messages, turnMessages...)
	}
	return PetChatHistoryResult{ThreadID: response.Thread.ID, Messages: messages}, nil
}

func parsePetCodexHistoryTurn(raw json.RawMessage, turnIndex int, localImageRoots ...[]string) ([]PetChatHistoryMessage, error) {
	var turn struct {
		ID             string            `json:"id"`
		Status         string            `json:"status"`
		CreatedAt      json.RawMessage   `json:"createdAt"`
		CreatedAtSnake json.RawMessage   `json:"created_at"`
		Timestamp      json.RawMessage   `json:"timestamp"`
		Items          []json.RawMessage `json:"items"`
		Input          json.RawMessage   `json:"input"`
		Output         json.RawMessage   `json:"output"`
	}
	if err := json.Unmarshal(raw, &turn); err != nil {
		return nil, err
	}
	turn.ID = strings.TrimSpace(turn.ID)
	createdAt := firstPetCodexTimestamp(turn.CreatedAt, turn.CreatedAtSnake, turn.Timestamp)
	items := turn.Items
	if len(items) == 0 {
		// 兼容早期 app-server/fixture 把输入输出直接挂在 turn 上的形态；
		// 当前 Codex 通常使用 items，这个分支只负责读取旧数据，不改变主协议。
		for _, candidate := range []json.RawMessage{turn.Input, turn.Output} {
			if len(candidate) == 0 || string(candidate) == "null" {
				continue
			}
			items = append(items, candidate)
		}
	}

	messages := make([]PetChatHistoryMessage, 0, len(items))
	for itemIndex, rawItem := range items {
		message, ok, err := parsePetCodexHistoryItem(rawItem, turn.ID, turn.Status, createdAt, turnIndex, itemIndex, localImageRoots...)
		if err != nil {
			return nil, err
		}
		if ok {
			messages = append(messages, message)
		}
	}
	return messages, nil
}

func parsePetCodexHistoryItem(raw json.RawMessage, turnID, turnStatus string, turnCreatedAt int64, turnIndex, itemIndex int, localImageRoots ...[]string) (PetChatHistoryMessage, bool, error) {
	var item map[string]json.RawMessage
	if err := json.Unmarshal(raw, &item); err != nil {
		return PetChatHistoryMessage{}, false, err
	}
	typeName := strings.ToLower(strings.TrimSpace(jsonStringField(item, "type")))
	role := strings.ToLower(strings.TrimSpace(jsonStringField(item, "role")))
	switch typeName {
	case "usermessage", "user_message", "user":
		role = "user"
	case "agentmessage", "agent_message", "assistant", "assistantmessage", "assistant_message":
		role = "assistant"
	default:
		if role != "user" && role != "assistant" {
			return PetChatHistoryMessage{}, false, nil
		}
	}
	content := petCodexHistoryText(item)
	images := petCodexHistoryImages(item, localImageRoots...)
	if strings.TrimSpace(content) == "" && len(images) == 0 {
		return PetChatHistoryMessage{}, false, nil
	}
	messageID := strings.TrimSpace(jsonStringField(item, "id"))
	if messageID == "" {
		messageID = fmt.Sprintf("turn-%d-item-%d", turnIndex, itemIndex)
		if turnID != "" {
			messageID = turnID + "-item-" + strconv.Itoa(itemIndex)
		}
	}
	createdAt := firstPetCodexTimestamp(item["createdAt"], item["created_at"], item["timestamp"])
	if createdAt == 0 {
		createdAt = turnCreatedAt
	}
	return PetChatHistoryMessage{
		ID:        messageID,
		Role:      role,
		Content:   strings.TrimSpace(content),
		Images:    images,
		CreatedAt: createdAt,
		Status:    strings.TrimSpace(turnStatus),
	}, true, nil
}

func jsonStringField(fields map[string]json.RawMessage, key string) string {
	var value string
	if raw := fields[key]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &value)
	}
	return value
}

func petCodexHistoryText(fields map[string]json.RawMessage) string {
	for _, key := range []string{"text", "content", "message", "input", "output"} {
		if text := petCodexHistoryTextValue(fields[key]); strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

func petCodexHistoryTextValue(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil {
		for _, key := range []string{"text", "content", "message"} {
			if value := petCodexHistoryTextValue(fields[key]); strings.TrimSpace(value) != "" {
				return value
			}
		}
		return ""
	}
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) == nil {
		parts := make([]string, 0, len(values))
		for _, value := range values {
			if text := petCodexHistoryTextValue(value); strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "")
	}
	return ""
}

func petCodexHistoryImages(fields map[string]json.RawMessage, localImageRoots ...[]string) []PetAIImage {
	var content []json.RawMessage
	if raw := fields["content"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &content)
	}
	images := make([]PetAIImage, 0)
	for _, block := range content {
		var value map[string]json.RawMessage
		if json.Unmarshal(block, &value) != nil {
			continue
		}
		typeName := strings.ToLower(strings.TrimSpace(jsonStringField(value, "type")))
		if typeName == "localimage" || typeName == "local_image" {
			roots := []string(nil)
			if len(localImageRoots) > 0 {
				roots = localImageRoots[0]
			}
			mediaType := jsonStringField(value, "mediaType")
			if mediaType == "" {
				mediaType = jsonStringField(value, "mimeType")
			}
			if image, ok := readPetCodexLocalImage(jsonStringField(value, "path"), mediaType, roots); ok {
				images = append(images, image)
			}
			continue
		}
		if typeName != "image" && typeName != "input_image" && typeName != "image_url" {
			continue
		}
		url := jsonStringField(value, "url")
		if url == "" {
			url = jsonStringField(value, "imageUrl")
		}
		if url == "" {
			if nested, ok := value["image_url"]; ok {
				var nestedFields map[string]json.RawMessage
				if json.Unmarshal(nested, &nestedFields) == nil {
					url = jsonStringField(nestedFields, "url")
				}
			}
		}
		mediaType, data, ok := splitPetCodexDataURL(url)
		if ok {
			images = append(images, PetAIImage{Data: data, MediaType: mediaType})
		}
	}
	return images
}

func splitPetCodexDataURL(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "data:") {
		return "", "", false
	}
	separator := strings.Index(value, ";base64,")
	if separator <= len("data:") {
		return "", "", false
	}
	mediaType := strings.TrimSpace(value[len("data:"):separator])
	data := strings.TrimSpace(value[separator+len(";base64,"):])
	if mediaType == "" || data == "" {
		return "", "", false
	}
	return mediaType, data, true
}

func parsePetCodexTimestamp(raw json.RawMessage) int64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if decoder.Decode(&number) == nil {
		if value, err := number.Int64(); err == nil {
			return normalizePetCodexTimestamp(value)
		}
		if value, err := strconv.ParseFloat(number.String(), 64); err == nil {
			return normalizePetCodexTimestamp(int64(value))
		}
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		if parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
			return normalizePetCodexTimestamp(parsed)
		}
		if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value)); err == nil {
			return parsed.UnixMilli()
		}
	}
	return 0
}

func firstPetCodexTimestamp(values ...json.RawMessage) int64 {
	for _, value := range values {
		if timestamp := parsePetCodexTimestamp(value); timestamp > 0 {
			return timestamp
		}
	}
	return 0
}

func normalizePetCodexTimestamp(value int64) int64 {
	if value <= 0 {
		return 0
	}
	// app-server/旧历史可能返回 Unix 秒、毫秒或微秒；统一成前端 Date 使用的毫秒。
	if value < 100_000_000_000 {
		return value * 1000
	}
	if value > 100_000_000_000_000 {
		return value / 1000
	}
	return value
}

var _ PetChatHistoryRuntime = (*PetCodexRuntime)(nil)
