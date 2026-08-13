package services

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

type projectManagerAggregate struct {
	Projects []ProjectSummary `json:"projects"`
	Sessions []SessionSummary `json:"sessions"`
}

type projectManagerSessionIndexEntry struct {
	ID         string    `json:"id"`
	ThreadName string    `json:"thread_name"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type projectManagerSessionState struct {
	SessionID           string
	SourceName          string
	DisplayName         string
	ProjectPath         string
	ProjectSource       string
	Summary             string
	LatestUserMessage   string
	LatestUserMessageAt time.Time
	UpdatedAt           time.Time
	WindowID            string
	Cwd                 string
	LastCapturePath     string
}

type projectManagerSessionFileEntry struct {
	SessionID           string
	Path                string
	ThreadName          string
	Cwd                 string
	ProjectPath         string
	ProjectSource       string
	Summary             string
	LatestUserMessage   string
	LatestUserMessageAt time.Time
	UpdatedAt           time.Time
	IsRollout           bool
}

type projectManagerProjectState struct {
	Path              string
	SourceName        string
	DisplayName       string
	RunCommand        string
	UpdatedAt         time.Time
	Sessions          []*projectManagerSessionState
	CodexProviderID   int64
	CodexProviderName string
	CodexProviderAuto bool
}

func (s *ProjectManagerService) scanProjectManagerData() (projectManagerAggregate, error) {
	// 保留这个入口作为兼容壳，但实际实现统一走新的全量缓存构建链，
	// 避免项目管理出现“两套扫描真相”继续分叉生长。
	cache, err := s.buildProjectManagerSnapshotCache(nil)
	if err != nil {
		return projectManagerAggregate{}, err
	}
	return projectManagerAggregate{
		Projects: cache.Snapshot.Projects,
		Sessions: cache.Snapshot.Sessions,
	}, nil
}

func (s *ProjectManagerService) readProjectManagerSessionIndex() ([]projectManagerSessionIndexEntry, error) {
	home, err := getUserHomeDir()
	if err != nil {
		return nil, err
	}

	path := filepath.Join(home, ".codex", "session_index.jsonl")
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []projectManagerSessionIndexEntry{}, nil
		}
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// session_index 一行很短，但这里留个余量，省得后续炸 Scanner 限制。
	buf := make([]byte, 0, 1024)
	scanner.Buffer(buf, 1024*1024)

	entries := make([]projectManagerSessionIndexEntry, 0, 256)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
			UpdatedAt  string `json:"updated_at"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		updatedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(entry.UpdatedAt))
		if err != nil {
			updatedAt = time.Time{}
		}
		entries = append(entries, projectManagerSessionIndexEntry{
			ID:         strings.TrimSpace(entry.ID),
			ThreadName: strings.TrimSpace(entry.ThreadName),
			UpdatedAt:  updatedAt,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return entries, nil
}

func (s *ProjectManagerService) enrichProjectManagerSessionsFromCodexSessions(
	sessions map[string]*projectManagerSessionState,
	store projectManagerStore,
	sessionFiles []projectManagerSessionFileEntry,
	indexByID map[string]projectManagerSessionIndexEntry,
) error {
	if len(sessionFiles) == 0 {
		return nil
	}

	for _, fileEntry := range sessionFiles {
		sessionID := strings.TrimSpace(fileEntry.SessionID)
		if sessionID == "" {
			continue
		}
		if projectManagerSessionIsHidden(store, sessionID) {
			continue
		}

		state := sessions[sessionID]
		if state == nil {
			state = buildProjectManagerSessionState(sessionID, store, indexByID[sessionID], fileEntry.ThreadName)
			sessions[sessionID] = state
		}

		cwd := strings.TrimSpace(fileEntry.Cwd)
		projectPath := strings.TrimSpace(fileEntry.ProjectPath)
		projectSource := strings.TrimSpace(fileEntry.ProjectSource)
		summary := strings.TrimSpace(fileEntry.Summary)
		latestUserMessage := strings.TrimSpace(fileEntry.LatestUserMessage)
		latestUserMessageAt := fileEntry.LatestUserMessageAt
		updatedAt := fileEntry.UpdatedAt
		if state.Cwd == "" && cwd != "" {
			state.Cwd = cwd
		}
		if state.ProjectPath == "" && projectPath != "" {
			state.ProjectPath = normalizeProjectManagerProjectPath(projectPath)
			state.ProjectSource = projectSource
		} else if state.ProjectPath == "" && cwd != "" {
			// 兼容旧数据：如果源文件里还没解析出更明确的项目根，才退回到 cwd。
			// rollout 的真实项目根优先走 workspace_roots，避免家目录 cwd 把项目归属带沟里。
			state.ProjectPath = normalizeProjectManagerProjectPath(cwd)
			state.ProjectSource = "cwd"
		}
		if state.Summary == "" && summary != "" {
			state.Summary = summary
			_ = s.store.saveSessionMetadata(sessionID, func(meta *projectManagerSessionMeta) bool {
				if strings.TrimSpace(meta.Summary) == summary {
					return false
				}
				meta.Summary = summary
				return true
			})
		}
		if latestUserMessage != "" {
			// 同一会话可能同时存在主文件和 rollout，必须按消息时间聚合，
			// 不能让目录遍历顺序决定卡片最终展示哪一条用户消息。
			if latestUserMessageAt.IsZero() {
				latestUserMessageAt = updatedAt
			}
			if state.LatestUserMessage == "" || latestUserMessageAt.After(state.LatestUserMessageAt) {
				state.LatestUserMessage = latestUserMessage
				state.LatestUserMessageAt = latestUserMessageAt
			}
		}
		if updatedAt.After(state.UpdatedAt) {
			state.UpdatedAt = updatedAt
		}

		if state.SourceName == "" && strings.TrimSpace(fileEntry.ThreadName) != "" {
			state.SourceName = strings.TrimSpace(fileEntry.ThreadName)
		}
		if state.DisplayName == "" && strings.TrimSpace(fileEntry.ThreadName) != "" {
			state.DisplayName = strings.TrimSpace(fileEntry.ThreadName)
		}

		if indexEntry, ok := indexByID[sessionID]; ok {
			if state.SourceName == "" && strings.TrimSpace(indexEntry.ThreadName) != "" {
				state.SourceName = strings.TrimSpace(indexEntry.ThreadName)
			}
			if meta, metaOK := store.Sessions[sessionID]; !metaOK || strings.TrimSpace(meta.DisplayName) == "" {
				if state.DisplayName == "" && strings.TrimSpace(indexEntry.ThreadName) != "" {
					state.DisplayName = strings.TrimSpace(indexEntry.ThreadName)
				}
			}
			if indexEntry.UpdatedAt.After(state.UpdatedAt) {
				state.UpdatedAt = indexEntry.UpdatedAt
			}
		}

		if state.SourceName == "" {
			state.SourceName = sessionID
		}
		if state.DisplayName == "" {
			state.DisplayName = state.SourceName
		}
	}

	return nil
}

func buildProjectManagerSessionState(
	sessionID string,
	store projectManagerStore,
	indexEntry projectManagerSessionIndexEntry,
	threadName string,
) *projectManagerSessionState {
	sourceName := strings.TrimSpace(threadName)
	if sourceName == "" {
		sourceName = strings.TrimSpace(indexEntry.ThreadName)
	}
	displayName := sourceName
	updatedAt := indexEntry.UpdatedAt

	state := &projectManagerSessionState{
		SessionID:   sessionID,
		SourceName:  sourceName,
		DisplayName: displayName,
		UpdatedAt:   updatedAt,
	}
	if meta, ok := store.Sessions[sessionID]; ok {
		if trimmed := strings.TrimSpace(meta.DisplayName); trimmed != "" {
			state.DisplayName = trimmed
		}
		if trimmed := strings.TrimSpace(meta.Summary); trimmed != "" {
			state.Summary = trimmed
		}
		if trimmed := strings.TrimSpace(meta.WindowID); trimmed != "" {
			state.WindowID = trimmed
		}
	}
	if state.SourceName == "" {
		state.SourceName = sessionID
	}
	if state.DisplayName == "" {
		state.DisplayName = state.SourceName
	}
	return state
}

func (s *ProjectManagerService) enrichProjectManagerSessionsFromCaptures(
	sessions map[string]*projectManagerSessionState,
	store projectManagerStore,
) error {
	home, err := getUserHomeDir()
	if err != nil {
		return err
	}

	root := filepath.Join(home, appSettingsDir, requestCaptureDirName, "codex")
	if !FileExists(root) {
		return nil
	}

	return filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}

		record, err := readProjectManagerCapture(path)
		if err != nil {
			return nil
		}
		sessionID := strings.TrimSpace(record.SessionID)
		if sessionID == "" {
			return nil
		}
		if projectManagerSessionIsHidden(store, sessionID) {
			return nil
		}

		state, ok := sessions[sessionID]
		if !ok {
			state = &projectManagerSessionState{
				SessionID:   sessionID,
				SourceName:  sessionID,
				DisplayName: sessionID,
			}
			if meta, metaOK := store.Sessions[sessionID]; metaOK {
				if trimmed := strings.TrimSpace(meta.DisplayName); trimmed != "" {
					state.DisplayName = trimmed
				}
				if trimmed := strings.TrimSpace(meta.Summary); trimmed != "" {
					state.Summary = trimmed
				}
				if trimmed := strings.TrimSpace(meta.WindowID); trimmed != "" {
					state.WindowID = trimmed
				}
			}
			sessions[sessionID] = state
		}

		capturedAt, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(record.CapturedAt))
		isNewerCapture := capturedAt.After(state.UpdatedAt)
		if isNewerCapture {
			state.UpdatedAt = capturedAt
		}

		if windowID := extractProjectManagerWindowID(record.Raw); windowID != "" {
			state.WindowID = windowID
			_ = s.store.saveSessionMetadata(sessionID, func(meta *projectManagerSessionMeta) bool {
				if strings.TrimSpace(meta.WindowID) == windowID {
					return false
				}
				meta.WindowID = windowID
				return true
			})
		}

		if summary := extractProjectManagerFirstUserMessage(record.Raw); state.Summary == "" && summary != "" {
			state.Summary = summary
			_ = s.store.saveSessionMetadata(sessionID, func(meta *projectManagerSessionMeta) bool {
				if strings.TrimSpace(meta.Summary) == summary {
					return false
				}
				meta.Summary = summary
				return true
			})
		}

		if projectRoot, source := extractProjectManagerProjectRoot(record.Raw); projectRoot != "" {
			normalized := normalizeProjectManagerProjectPath(projectRoot)
			if normalized != "" {
				state.ProjectPath = normalized
				state.ProjectSource = source
				if state.Cwd == "" && source == "cwd" {
					state.Cwd = normalized
				}
			}
		} else if state.ProjectPath == "" && strings.TrimSpace(record.ProjectID) != "" && record.ProjectID != unknownProjectCaptureID {
			state.ProjectPath = normalizeProjectManagerProjectPath(record.ProjectID)
			state.ProjectSource = "capture.project_id"
		}

		if state.LastCapturePath == "" || isNewerCapture {
			state.LastCapturePath = path
		}
		return nil
	})
}

func (s *ProjectManagerService) groupProjectManagerProjects(
	sessions map[string]*projectManagerSessionState,
	store projectManagerStore,
) map[string]*projectManagerProjectState {
	projects := make(map[string]*projectManagerProjectState)

	for _, session := range sessions {
		if session == nil {
			continue
		}
		projectPath := session.ProjectPath
		if projectPath == "" {
			projectPath = normalizeProjectManagerProjectPath(session.Cwd)
		}
		if projectPath == "" {
			projectPath = unknownProjectCaptureID
		}

		// 同一个 Windows 路径可能被 Codex 用不同盘符/目录大小写记录，分组时必须视为同一项目。
		projectKey := projectPath
		if runtime.GOOS == "windows" {
			projectKey = strings.ToLower(projectPath)
		}
		project := projects[projectKey]
		if project == nil {
			sourceName := filepath.Base(projectPath)
			if projectPath == unknownProjectCaptureID {
				sourceName = "Unknown Project"
			}
			displayName := sourceName
			var projectMeta projectManagerProjectMeta
			if meta, ok := projectManagerProjectMetaFromStore(store, projectPath); ok {
				projectMeta = meta
				if strings.TrimSpace(meta.DisplayName) != "" {
					displayName = strings.TrimSpace(meta.DisplayName)
				}
			}
			codexProviderID, codexProviderName := resolveProjectManagerCodexProvider(store, projectPath)
			codexProviderAuto := resolveProjectManagerCodexProviderAutoFallback(store, projectPath)
			project = &projectManagerProjectState{
				Path:              projectPath,
				SourceName:        sourceName,
				DisplayName:       displayName,
				RunCommand:        strings.TrimSpace(projectMeta.RunCommand),
				CodexProviderID:   codexProviderID,
				CodexProviderName: codexProviderName,
				CodexProviderAuto: codexProviderAuto,
			}
			projects[projectKey] = project
		}

		project.Sessions = append(project.Sessions, session)
		if session.UpdatedAt.After(project.UpdatedAt) {
			project.UpdatedAt = session.UpdatedAt
		}
	}

	return projects
}

type projectManagerCaptureFile struct {
	CapturedAt string          `json:"captured_at"`
	ProjectID  string          `json:"project_id"`
	SessionID  string          `json:"session_id"`
	Request    json.RawMessage `json:"request"`
	Raw        []byte          `json:"-"`
}

func readProjectManagerCapture(path string) (projectManagerCaptureFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return projectManagerCaptureFile{}, err
	}

	var file projectManagerCaptureFile
	if err := json.Unmarshal(data, &file); err != nil {
		return projectManagerCaptureFile{}, err
	}
	file.Raw = data
	return file, nil
}

func extractProjectManagerWindowID(raw []byte) string {
	candidates := []string{
		"request.headers.X-Codex-Window-Id",
		"request.headers.x-codex-window-id",
		"request.body.client_metadata.x-codex-window-id",
	}
	for _, path := range candidates {
		if value := strings.TrimSpace(gjson.GetBytes(raw, path).String()); value != "" {
			return value
		}
	}

	if rawMeta := strings.TrimSpace(gjson.GetBytes(raw, "request.headers.X-Codex-Turn-Metadata").String()); rawMeta != "" {
		if value := strings.TrimSpace(gjson.Get(rawMeta, "window_id").String()); value != "" {
			return value
		}
	}
	return ""
}

func extractProjectManagerProjectRoot(raw []byte) (string, string) {
	// 请求体里经常会把 tool 参数再包成一层 JSON 字符串，不做递归搜索就只能抓瞎。
	if payload := gjson.GetBytes(raw, "request.body"); payload.Exists() {
		if value, source := extractProjectManagerProjectRootFromValue(payload.Value(), 0); value != "" {
			return value, source
		}
	}

	paths := []struct {
		Path   string
		Source string
	}{
		{Path: "request.body.project_root_path", Source: "project_root_path"},
		{Path: "request.body.projectRootPath", Source: "project_root_path"},
		{Path: "request.body.cwd", Source: "cwd"},
		{Path: "request.body.workdir", Source: "workdir"},
		{Path: "request.body.workspace_root", Source: "workspace_root"},
	}
	for _, candidate := range paths {
		if value := strings.TrimSpace(gjson.GetBytes(raw, candidate.Path).String()); value != "" {
			return value, candidate.Source
		}
	}

	return "", ""
}

func extractProjectManagerProjectRootFromValue(value any, depth int) (string, string) {
	if depth > maxCaptureDetectionDepth {
		return "", ""
	}

	switch typed := value.(type) {
	case map[string]any:
		for _, key := range sortedCaptureMapKeys(typed) {
			normalized := normalizeCaptureKey(key)
			switch normalized {
			case "projectrootpath":
				if text, ok := typed[key].(string); ok && strings.TrimSpace(text) != "" {
					return text, "project_root_path"
				}
			case "cwd":
				if text, ok := typed[key].(string); ok && strings.TrimSpace(text) != "" {
					return text, "cwd"
				}
			case "workspace", "workspaceid", "workspaceroot", "workdir":
				if text, ok := typed[key].(string); ok && strings.TrimSpace(text) != "" {
					return text, key
				}
			}
		}
		for _, key := range sortedCaptureMapKeys(typed) {
			if value, source := extractProjectManagerProjectRootFromValue(typed[key], depth+1); value != "" {
				return value, source
			}
		}
	case []any:
		for _, item := range typed {
			if value, source := extractProjectManagerProjectRootFromValue(item, depth+1); value != "" {
				return value, source
			}
		}
	case string:
		if cwd := extractProjectManagerXMLTag(typed, "cwd"); cwd != "" {
			return cwd, "cwd"
		}
		if root := extractProjectManagerXMLTag(typed, "root"); root != "" {
			return root, "environment.root"
		}
		if value := extractProjectManagerProjectRootPathFromText(typed); value != "" {
			return value, "project_root_path"
		}
		if nested, ok := parseCaptureNestedJSON(typed); ok {
			return extractProjectManagerProjectRootFromValue(nested, depth+1)
		}
	}

	return "", ""
}

func extractProjectManagerProjectRootPathFromText(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}

	if json.Valid([]byte(text)) {
		value := strings.TrimSpace(gjson.Get(text, "project_root_path").String())
		if value != "" {
			return value
		}
	}

	idx := strings.Index(text, `"project_root_path"`)
	if idx < 0 {
		return ""
	}
	snippet := text[idx:]
	colonIdx := strings.Index(snippet, ":")
	if colonIdx < 0 {
		return ""
	}
	snippet = snippet[colonIdx+1:]
	firstQuote := strings.Index(snippet, `"`)
	if firstQuote < 0 {
		return ""
	}
	snippet = snippet[firstQuote+1:]
	endQuote := strings.Index(snippet, `"`)
	if endQuote < 0 {
		return ""
	}
	// 这里拿到的是 JSON 字符串字面量片段，Windows 路径里的反斜杠还带着转义；
	// 不做一次反解码，最终就会把 `C:\\foo` 当成真实路径返回，后续归并全乱套。
	return decodeProjectManagerJSONString(snippet[:endQuote])
}

func decodeProjectManagerJSONString(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	decoded, err := strconv.Unquote(`"` + value + `"`)
	if err == nil {
		return strings.TrimSpace(decoded)
	}
	return value
}

func extractProjectManagerXMLTag(content string, tag string) string {
	startTag := "<" + tag + ">"
	start := strings.Index(content, startTag)
	if start < 0 {
		return ""
	}
	start += len(startTag)
	endTag := "</" + tag + ">"
	end := strings.Index(content[start:], endTag)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(content[start : start+end])
}

func extractProjectManagerFirstUserMessage(raw []byte) string {
	if text := strings.TrimSpace(gjson.GetBytes(raw, "request.body.input.0.content.0.text").String()); text != "" {
		return projectManagerTrimSummary(text)
	}

	if text := strings.TrimSpace(gjson.GetBytes(raw, "request.body.input.0.content").String()); text != "" {
		return projectManagerTrimSummary(text)
	}

	return ""
}

func projectManagerTrimSummary(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "<") && strings.HasSuffix(trimmed, ">") {
			continue
		}
		filtered = append(filtered, trimmed)
		if len(filtered) >= 3 {
			break
		}
	}
	summary := strings.Join(filtered, " ")
	if len([]rune(summary)) > 120 {
		summary = string([]rune(summary)[:120]) + "..."
	}
	return summary
}

func (s *ProjectManagerService) readProjectManagerSessionFiles(
	indexByID map[string]projectManagerSessionIndexEntry,
) ([]projectManagerSessionFileEntry, error) {
	home, err := getUserHomeDir()
	if err != nil {
		return nil, err
	}

	baseDir := filepath.Join(home, ".codex", "sessions")
	if !FileExists(baseDir) {
		return []projectManagerSessionFileEntry{}, nil
	}

	result := make([]projectManagerSessionFileEntry, 0, 256)
	err = filepath.WalkDir(baseDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}

		sessionID, cwd, projectPath, projectSource, summary, latestUserMessage, latestUserMessageAt, updatedAt, err := scanProjectManagerCodexSessionFileDetails(path)
		if err != nil {
			return nil
		}
		if sessionID == "" {
			sessionID = extractProjectManagerSessionIDFromFileName(d.Name())
		}
		if sessionID == "" {
			return nil
		}

		threadName := ""
		if entry, ok := indexByID[sessionID]; ok {
			threadName = strings.TrimSpace(entry.ThreadName)
		}

		result = append(result, projectManagerSessionFileEntry{
			SessionID:           sessionID,
			Path:                path,
			ThreadName:          threadName,
			Cwd:                 cwd,
			ProjectPath:         projectPath,
			ProjectSource:       projectSource,
			Summary:             summary,
			LatestUserMessage:   latestUserMessage,
			LatestUserMessageAt: latestUserMessageAt,
			UpdatedAt:           updatedAt,
			IsRollout:           projectManagerIsRolloutSessionPath(path),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Path) < strings.ToLower(result[j].Path)
	})
	return result, nil
}

func extractProjectManagerSessionIDFromFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || !strings.HasSuffix(name, ".jsonl") {
		return ""
	}

	base := strings.TrimSuffix(name, ".jsonl")
	parts := strings.Split(base, "-")
	if len(parts) < 5 {
		return ""
	}

	candidate := strings.Join(parts[len(parts)-5:], "-")
	if !projectManagerLooksLikeSessionID(candidate) {
		return ""
	}
	return candidate
}

func projectManagerLooksLikeSessionID(value string) bool {
	if len(value) != 36 {
		return false
	}

	for index, r := range value {
		switch index {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
				return false
			}
		}
	}
	return true
}

func scanProjectManagerCodexSessionFileDetails(path string) (sessionID string, cwd string, projectPath string, projectSource string, summary string, latestUserMessage string, latestUserMessageAt time.Time, updatedAt time.Time, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", "", "", "", "", time.Time{}, time.Time{}, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 4*1024)
	// rollout 里可能包含大块 tool output / instructions，单行超过 Go Scanner 默认限制很常见。
	// 如果这里继续卡 1MB，文件会在后半段报 token too long，前面已经读到的 session_meta.cwd 也会被整文件丢弃。
	scanner.Buffer(buf, 16*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if projectPath == "" || projectSource == "cwd" {
			if roots := extractProjectManagerWorkspaceRootsFromLine(line); len(roots) > 0 {
				// rollout 的 turn_context 才会稳定给出 workspace_roots；
				// 这层必须在逐行扫描时统一处理，不能只绑死在 session_meta 分支里。
				// 如果前面已经用 session_meta.cwd 做了兜底，这里也必须覆盖回来，
				// 否则多工作区会被较早出现但不够精确的 cwd 抢走项目归属。
				projectPath = roots[0]
				projectSource = "workspace_roots"
			}
		}

		if gjson.Get(line, "type").String() == "session_meta" {
			if sessionID == "" {
				sessionID = strings.TrimSpace(gjson.Get(line, "payload.id").String())
			}
			if cwd == "" {
				cwd = strings.TrimSpace(gjson.Get(line, "payload.cwd").String())
			}
			if projectPath == "" && cwd != "" {
				// Codex 历史会话的 session_meta.cwd 是最早出现的项目上下文。
				// workspace_roots 仍然优先，因为多工作区场景 cwd 可能是家目录；但如果整条会话没有
				// workspace_roots，再不把 cwd 落成 ProjectPath，项目列表就会漏掉这类真实项目。
				projectPath = cwd
				projectSource = "cwd"
			}
			if ts := strings.TrimSpace(gjson.Get(line, "payload.timestamp").String()); ts != "" {
				if parsed, parseErr := time.Parse(time.RFC3339Nano, ts); parseErr == nil && parsed.After(updatedAt) {
					updatedAt = parsed
				}
			}
		}

		lineTimestamp := time.Time{}
		if ts := strings.TrimSpace(gjson.Get(line, "timestamp").String()); ts != "" {
			if parsed, parseErr := time.Parse(time.RFC3339Nano, ts); parseErr == nil {
				lineTimestamp = parsed
				if parsed.After(updatedAt) {
					updatedAt = parsed
				}
			}
		}

		if summary == "" && gjson.Get(line, "type").String() == "event_msg" && gjson.Get(line, "payload.type").String() == "user_message" {
			summary = projectManagerTrimSummary(gjson.Get(line, "payload.message").String())
		}

		if userMessage := extractProjectManagerSessionLineUserMessage(line); userMessage != "" {
			// JSONL 的行序就是事件落盘顺序；文件内取最后一条有效消息，
			// 时间戳仅用于随后跨主文件和 rollout 文件比较。
			latestUserMessage = userMessage
			latestUserMessageAt = lineTimestamp
		}

		if cwd != "" && summary != "" && !updatedAt.IsZero() {
			// 这里不提前 break，因为 updatedAt 需要尽量拿到真实最后一条时间。
		}
	}
	if err := scanner.Err(); err != nil {
		return "", "", "", "", "", "", time.Time{}, time.Time{}, err
	}

	return sessionID, cwd, projectPath, projectSource, summary, latestUserMessage, latestUserMessageAt, updatedAt, nil
}

func extractProjectManagerSessionLineUserMessage(line string) string {
	recordType := gjson.Get(line, "type").String()
	if recordType == "event_msg" && gjson.Get(line, "payload.type").String() == "user_message" {
		return projectManagerTrimSummary(gjson.Get(line, "payload.message").String())
	}
	if recordType != "response_item" || gjson.Get(line, "payload.type").String() != "message" || gjson.Get(line, "payload.role").String() != "user" {
		return ""
	}

	content := gjson.Get(line, "payload.content")
	if !content.IsArray() {
		return projectManagerTrimSummary(content.String())
	}

	// 新旧 Codex 会话都可能只有 response_item；这里只抽取文本片段，
	// 图片和其他结构化内容不能被序列化成 JSON 噪声塞进卡片预览。
	parts := make([]string, 0, 2)
	content.ForEach(func(_, item gjson.Result) bool {
		if text := strings.TrimSpace(item.Get("text").String()); text != "" {
			parts = append(parts, text)
		}
		return true
	})
	return projectManagerTrimSummary(strings.Join(parts, "\n"))
}

func extractProjectManagerWorkspaceRootsFromLine(line string) []string {
	paths := []string{
		"workspace_roots",
		"workspaceRoots",
		"payload.workspace_roots",
		"payload.workspaceRoots",
	}
	for _, path := range paths {
		result := gjson.Get(line, path)
		if !result.Exists() || !result.IsArray() {
			continue
		}
		roots := make([]string, 0, len(result.Array()))
		for _, item := range result.Array() {
			trimmed := strings.TrimSpace(item.String())
			if trimmed == "" {
				continue
			}
			roots = append(roots, trimmed)
		}
		if len(roots) > 0 {
			return roots
		}
	}
	return nil
}

func buildProjectManagerProjectSummaries(projects map[string]*projectManagerProjectState) []ProjectSummary {
	keys := make([]string, 0, len(projects))
	for key := range projects {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left := projects[keys[i]]
		right := projects[keys[j]]
		if !left.UpdatedAt.Equal(right.UpdatedAt) {
			return left.UpdatedAt.After(right.UpdatedAt)
		}
		return strings.ToLower(left.DisplayName) < strings.ToLower(right.DisplayName)
	})

	result := make([]ProjectSummary, 0, len(keys))
	for _, key := range keys {
		project := projects[key]
		if project == nil {
			continue
		}
		result = append(result, ProjectSummary{
			ID:                project.Path,
			Path:              project.Path,
			SourceName:        project.SourceName,
			DisplayName:       project.DisplayName,
			RunCommand:        project.RunCommand,
			UpdatedAt:         project.UpdatedAt.UnixMilli(),
			SessionCount:      len(project.Sessions),
			CodexProviderID:   project.CodexProviderID,
			CodexProviderName: project.CodexProviderName,
			CodexProviderAuto: project.CodexProviderAuto,
		})
	}
	return result
}

func buildProjectManagerSessionSummaries(projects map[string]*projectManagerProjectState) []SessionSummary {
	result := make([]SessionSummary, 0, 256)
	for _, project := range projects {
		if project == nil {
			continue
		}
		for _, session := range project.Sessions {
			if session == nil {
				continue
			}
			result = append(result, SessionSummary{
				ID:                session.SessionID,
				ProjectID:         project.Path,
				ProjectPath:       project.Path,
				ProjectName:       project.DisplayName,
				SourceName:        session.SourceName,
				DisplayName:       session.DisplayName,
				Summary:           session.Summary,
				LatestUserMessage: session.LatestUserMessage,
				UpdatedAt:         session.UpdatedAt.UnixMilli(),
				WindowID:          session.WindowID,
				Cwd:               session.Cwd,
				LastCapturePath:   session.LastCapturePath,
				ProjectSourceHint: session.ProjectSource,
			})
		}
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].UpdatedAt != result[j].UpdatedAt {
			return result[i].UpdatedAt > result[j].UpdatedAt
		}
		return strings.ToLower(result[i].DisplayName) < strings.ToLower(result[j].DisplayName)
	})

	return result
}

func projectManagerFindSession(sessions []SessionSummary, sessionID string) (SessionSummary, error) {
	for _, session := range sessions {
		if session.ID == sessionID {
			return session, nil
		}
	}
	return SessionSummary{}, fmt.Errorf("未找到会话: %s", sessionID)
}
