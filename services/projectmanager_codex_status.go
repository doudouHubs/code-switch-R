package services

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	projectManagerCodexStateFile        = "state.json"
	projectManagerCodexEventPoll        = 350 * time.Millisecond
	projectManagerCodexProcessPoll      = 2 * time.Second
	projectManagerCodexUnknownPIDMaxAge = 30 * time.Second
)

type projectManagerCodexStatusService struct {
	mu       sync.RWMutex
	sessions map[string]*CodexSessionRuntimeStatus
	monitor  CodexStatusMonitorInfo
	app      *application.App
	stop     chan struct{}
	started  bool
	stopped  bool
}

func newProjectManagerCodexStatusService() *projectManagerCodexStatusService {
	return &projectManagerCodexStatusService{
		sessions: make(map[string]*CodexSessionRuntimeStatus),
		stop:     make(chan struct{}),
	}
}

func (s *ProjectManagerService) SetApp(app *application.App) {
	if s == nil || s.codexStatus == nil {
		return
	}
	s.codexStatus.mu.Lock()
	s.codexStatus.app = app
	s.codexStatus.mu.Unlock()
}

func (s *ProjectManagerService) StartCodexStatusMonitor() {
	if s == nil || s.codexStatus == nil {
		return
	}
	s.codexStatus.start()
}

func (s *ProjectManagerService) StopCodexStatusMonitor() {
	if s == nil || s.codexStatus == nil {
		return
	}
	s.codexStatus.shutdown()
}

func (s *ProjectManagerService) GetCodexRuntimeStatusSnapshot() CodexRuntimeStatusSnapshot {
	if s == nil || s.codexStatus == nil {
		return CodexRuntimeStatusSnapshot{}
	}
	return s.codexStatus.snapshot()
}

func (s *projectManagerCodexStatusService) start() {
	s.mu.Lock()
	if s.started || s.stopped {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()

	if err := s.loadState(); err != nil && !os.IsNotExist(err) {
		log.Printf("[ProjectManager] 加载 Codex 状态缓存失败: %v", err)
	}
	go s.run()
	go func() {
		info, err := installProjectManagerCodexHooks()
		if err != nil {
			info.Error = err.Error()
		}
		s.mu.Lock()
		s.monitor = info
		for _, status := range s.sessions {
			status.AgentSupported = info.AgentHooksSupported
		}
		s.mu.Unlock()
		s.persistAndEmit()
		if err != nil {
			log.Printf("[ProjectManager] 自动安装 Codex 状态 Hook 失败: %v", err)
			return
		}
		log.Printf("[ProjectManager] Codex 状态 Hook 已就绪 version=%q agent_hooks=%t", info.CodexVersion, info.AgentHooksSupported)
	}()
}

func (s *projectManagerCodexStatusService) shutdown() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	close(s.stop)
	s.mu.Unlock()
}

func (s *projectManagerCodexStatusService) run() {
	eventTicker := time.NewTicker(projectManagerCodexEventPoll)
	processTicker := time.NewTicker(projectManagerCodexProcessPoll)
	defer eventTicker.Stop()
	defer processTicker.Stop()

	// 启动后先消费应用关闭期间积压的事件，不必等第一个 ticker。
	s.consumeEvents()
	for {
		select {
		case <-eventTicker.C:
			s.consumeEvents()
		case <-processTicker.C:
			if s.reconcileProcessesAndTranscripts() {
				s.persistAndEmit()
			}
		case <-s.stop:
			return
		}
	}
}

func (s *projectManagerCodexStatusService) consumeEvents() {
	root, err := projectManagerCodexEventRootPath()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[ProjectManager] 扫描 Codex Hook 事件失败: %v", err)
		}
		return
	}

	changed := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		var event projectManagerCodexHookEvent
		if err := ReadJSONFile(path, &event); err != nil {
			log.Printf("[ProjectManager] 丢弃损坏的 Codex Hook 事件 path=%s err=%v", path, err)
			_ = os.Remove(path)
			continue
		}
		if s.applyEvent(event) {
			changed = true
		}
		_ = os.Remove(path)
	}
	if changed {
		s.persistAndEmit()
	}
}

func (s *projectManagerCodexStatusService) applyEvent(event projectManagerCodexHookEvent) bool {
	sessionID := strings.TrimSpace(event.SessionID)
	if sessionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.sessions[sessionID]
	if status == nil {
		status = &CodexSessionRuntimeStatus{
			SessionID:      sessionID,
			State:          CodexRuntimeNotLoaded,
			Monitored:      true,
			AgentSupported: s.monitor.AgentHooksSupported,
			activeAgentIDs: make(map[string]struct{}),
		}
		s.sessions[sessionID] = status
	}
	if event.ReceivedUnixNano > 0 && event.ReceivedUnixNano < status.LastEventNano {
		return false
	}
	if status.activeAgentIDs == nil {
		status.activeAgentIDs = make(map[string]struct{})
	}

	status.Monitored = true
	status.LastEvent = event.HookEventName
	status.UpdatedAt = event.ReceivedAt
	status.LastEventNano = event.ReceivedUnixNano
	if event.Cwd != "" {
		status.ProjectPath = normalizeProjectManagerProjectPath(event.Cwd)
	}
	if event.TurnID != "" {
		status.TurnID = event.TurnID
	}
	if event.TranscriptPath != "" {
		status.TranscriptPath = event.TranscriptPath
	}
	if event.CodexPID != 0 {
		status.CodexPID = event.CodexPID
		status.CodexStartedAt = event.CodexStartedAt
	}

	switch strings.ToLower(strings.TrimSpace(event.HookEventName)) {
	case "sessionstart":
		status.State = CodexRuntimeIdle
		status.TurnStatus = ""
		status.LastError = ""
		status.ActiveAgents = 0
		status.activeAgentIDs = make(map[string]struct{})
	case "userpromptsubmit":
		status.State = CodexRuntimeActive
		status.TurnStatus = "in_progress"
		status.LastError = ""
	case "permissionrequest":
		status.State = CodexRuntimeWaitingApproval
		status.TurnStatus = "in_progress"
	case "pretooluse":
		if strings.EqualFold(strings.TrimSpace(event.ToolName), "request_user_input") {
			status.State = CodexRuntimeWaitingUserInput
		} else if status.TurnStatus == "in_progress" {
			status.State = CodexRuntimeActive
		}
	case "posttooluse":
		if status.TurnStatus == "in_progress" {
			status.State = CodexRuntimeActive
		}
	case "stop":
		// Plan 模式的确认框在回合完成后由 TUI 本地显示；核心不会再发用户输入事件。
		// 保留 completed 可准确表达回合已结束，同时用 waiting_user_input 提示用户仍需决定下一步。
		if event.PlanImplementationPending {
			status.State = CodexRuntimeWaitingUserInput
		} else {
			status.State = CodexRuntimeIdle
		}
		status.TurnStatus = "completed"
		status.LastError = ""
		status.ActiveAgents = 0
		status.activeAgentIDs = make(map[string]struct{})
	case "subagentstart":
		agentID := strings.TrimSpace(event.AgentID)
		if agentID == "" {
			agentID = event.EventID
		}
		status.activeAgentIDs[agentID] = struct{}{}
		status.ActiveAgents = len(status.activeAgentIDs)
		status.State = CodexRuntimeActive
		status.TurnStatus = "in_progress"
	case "subagentstop":
		if event.AgentID != "" {
			delete(status.activeAgentIDs, event.AgentID)
		} else if status.ActiveAgents > 0 {
			status.ActiveAgents--
		}
		if len(status.activeAgentIDs) > 0 || status.ActiveAgents == 0 {
			status.ActiveAgents = len(status.activeAgentIDs)
		}
	}
	return true
}

func (s *projectManagerCodexStatusService) reconcileProcessesAndTranscripts() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	now := time.Now().UTC()
	for _, status := range s.sessions {
		if reconcileProjectManagerCodexTranscript(status) {
			changed = true
		}
		if status.State == CodexRuntimeNotLoaded {
			continue
		}
		alive := status.CodexPID != 0 && isProjectManagerCodexProcessAlive(status.CodexPID, status.CodexStartedAt)
		if status.CodexPID == 0 && now.Sub(time.UnixMilli(status.UpdatedAt)) < projectManagerCodexUnknownPIDMaxAge {
			continue
		}
		if alive {
			continue
		}
		if status.TurnStatus == "in_progress" {
			status.TurnStatus = "interrupted"
		}
		status.State = CodexRuntimeNotLoaded
		status.ActiveAgents = 0
		status.activeAgentIDs = make(map[string]struct{})
		status.UpdatedAt = now.UnixMilli()
		changed = true
	}
	return changed
}

func reconcileProjectManagerCodexTranscript(status *CodexSessionRuntimeStatus) bool {
	path := strings.TrimSpace(status.TranscriptPath)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.ModTime().UnixNano() == status.TranscriptMTime {
		return false
	}
	status.TranscriptMTime = info.ModTime().UnixNano()
	data, err := readProjectManagerCodexFileTail(path, 128*1024)
	if err != nil {
		return false
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	completedFunctionCalls := make(map[string]struct{})
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if line == "" {
			continue
		}
		itemType := strings.ToLower(gjson.Get(line, "type").String())
		if itemType == "response_item" {
			if strings.EqualFold(gjson.Get(line, "payload.type").String(), "function_call_output") {
				if callID := strings.TrimSpace(gjson.Get(line, "payload.call_id").String()); callID != "" {
					completedFunctionCalls[callID] = struct{}{}
				}
			}
			continue
		}
		if itemType != "event_msg" {
			continue
		}
		payloadType := strings.ToLower(gjson.Get(line, "payload.type").String())
		turnID := strings.TrimSpace(gjson.Get(line, "payload.turn_id").String())
		if status.TurnID != "" && turnID != "" && status.TurnID != turnID {
			continue
		}
		switch payloadType {
		case "request_user_input":
			callID := strings.TrimSpace(gjson.Get(line, "payload.call_id").String())
			_, resolved := completedFunctionCalls[callID]
			if callID != "" && resolved {
				if status.State != CodexRuntimeWaitingUserInput {
					return false
				}
				status.State = CodexRuntimeActive
				status.UpdatedAt = time.Now().UnixMilli()
				return true
			}
			if status.State == CodexRuntimeWaitingUserInput && status.TurnStatus == "in_progress" {
				return false
			}
			// 0.122-0.135 的 Function Tool 不进入通用 PreToolUse 管线，
			// 只能从已持久化的请求事件判断当前是否卡在用户输入上。
			status.State = CodexRuntimeWaitingUserInput
			status.TurnStatus = "in_progress"
			status.UpdatedAt = time.Now().UnixMilli()
			return true
		case "task_complete", "turn_complete":
			// Stop Hook 已识别到计划确认框时，转录里的完成事件不能把待确认状态回填为 idle。
			if status.TurnStatus == "completed" &&
				(status.State == CodexRuntimeIdle || status.State == CodexRuntimeWaitingUserInput) {
				return false
			}
			status.TurnStatus = "completed"
			status.State = CodexRuntimeIdle
			status.LastError = ""
			status.UpdatedAt = time.Now().UnixMilli()
			return true
		case "turn_aborted":
			status.TurnStatus = "interrupted"
			status.State = CodexRuntimeIdle
			status.UpdatedAt = time.Now().UnixMilli()
			return true
		case "error":
			if gjson.Get(line, "payload.will_retry").Bool() {
				continue
			}
			status.TurnStatus = "failed"
			status.State = CodexRuntimeSystemError
			status.LastError = strings.TrimSpace(gjson.Get(line, "payload.message").String())
			status.UpdatedAt = time.Now().UnixMilli()
			return true
		case "task_started", "turn_started":
			return false
		}
	}
	return false
}

func readProjectManagerCodexFileTail(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	start := info.Size() - maxBytes
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, 0); err != nil {
		return nil, err
	}
	// File.Read 不保证一次填满缓冲区；这里已经通过 Seek 限制为尾部窗口，
	// 用 ReadAll 才能确保一次 reconciliation 看到完整的 JSONL 尾段。
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	if start > 0 {
		if newline := strings.IndexByte(string(data), '\n'); newline >= 0 {
			data = data[newline+1:]
		}
	}
	return data, nil
}

func (s *projectManagerCodexStatusService) snapshot() CodexRuntimeStatusSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sessions := make([]CodexSessionRuntimeStatus, 0, len(s.sessions))
	for _, status := range s.sessions {
		copyStatus := *status
		copyStatus.activeAgentIDs = nil
		sessions = append(sessions, copyStatus)
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].UpdatedAt != sessions[j].UpdatedAt {
			return sessions[i].UpdatedAt > sessions[j].UpdatedAt
		}
		return sessions[i].SessionID < sessions[j].SessionID
	})
	projects := aggregateProjectManagerCodexProjects(sessions)
	return CodexRuntimeStatusSnapshot{
		Monitor:   s.monitor,
		Sessions:  sessions,
		Projects:  projects,
		UpdatedAt: time.Now().UnixMilli(),
	}
}

func aggregateProjectManagerCodexProjects(sessions []CodexSessionRuntimeStatus) []CodexProjectRuntimeStatus {
	byPath := make(map[string]*CodexProjectRuntimeStatus)
	representativeUpdatedAt := make(map[string]int64)
	for _, session := range sessions {
		path := normalizeProjectManagerProjectPath(session.ProjectPath)
		if path == "" {
			continue
		}
		key := strings.ToLower(path)
		project := byPath[key]
		if project == nil {
			project = &CodexProjectRuntimeStatus{ProjectPath: path, State: CodexRuntimeNotLoaded}
			byPath[key] = project
		}
		sessionPriority := codexRuntimeStatePriority(session.State)
		projectPriority := codexRuntimeStatePriority(project.State)
		if sessionPriority > projectPriority {
			project.State = session.State
			project.LatestSessionID = session.SessionID
			project.LatestSessionState = session.State
			representativeUpdatedAt[key] = session.UpdatedAt
		} else if sessionPriority == projectPriority && session.UpdatedAt >= representativeUpdatedAt[key] {
			// tooltip 必须引用真正决定聚合灯色的会话，不能拿一个更新但低优先级的 idle 会话解释红灯。
			project.LatestSessionID = session.SessionID
			project.LatestSessionState = session.State
			representativeUpdatedAt[key] = session.UpdatedAt
		}
		if session.State == CodexRuntimeActive {
			project.ActiveSessions++
		}
		if session.State == CodexRuntimeWaitingApproval || session.State == CodexRuntimeWaitingUserInput {
			project.WaitingSessions++
		}
		if session.State == CodexRuntimeSystemError {
			project.ErrorSessions++
		}
		if session.UpdatedAt >= project.UpdatedAt {
			project.UpdatedAt = session.UpdatedAt
		}
	}
	projects := make([]CodexProjectRuntimeStatus, 0, len(byPath))
	for _, project := range byPath {
		projects = append(projects, *project)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].ProjectPath < projects[j].ProjectPath })
	return projects
}

func codexRuntimeStatePriority(state CodexRuntimeState) int {
	switch state {
	case CodexRuntimeSystemError:
		return 6
	case CodexRuntimeWaitingApproval:
		return 5
	case CodexRuntimeWaitingUserInput:
		return 4
	case CodexRuntimeActive:
		return 3
	case CodexRuntimeIdle:
		return 2
	default:
		return 1
	}
}

func (s *projectManagerCodexStatusService) statePath() (string, error) {
	root, err := projectManagerCodexStatusRootPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, projectManagerCodexStateFile), nil
}

func (s *projectManagerCodexStatusService) loadState() error {
	path, err := s.statePath()
	if err != nil {
		return err
	}
	var snapshot CodexRuntimeStatusSnapshot
	if err := ReadJSONFile(path, &snapshot); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.monitor = snapshot.Monitor
	for index := range snapshot.Sessions {
		status := snapshot.Sessions[index]
		// PID 和启动时间不会持久化，应用重启后无法证明旧进程仍是同一个 Codex。
		// 立即降为 not_loaded，后续积压 Hook 会重新建立可信的实时状态。
		if status.TurnStatus == "in_progress" {
			status.TurnStatus = "interrupted"
		}
		status.State = CodexRuntimeNotLoaded
		status.ActiveAgents = 0
		status.activeAgentIDs = make(map[string]struct{})
		s.sessions[status.SessionID] = &status
	}
	return nil
}

func (s *projectManagerCodexStatusService) persistAndEmit() {
	snapshot := s.snapshot()
	if path, err := s.statePath(); err == nil {
		if err := AtomicWriteJSON(path, snapshot); err != nil {
			log.Printf("[ProjectManager] 持久化 Codex 状态失败: %v", err)
		}
	}
	s.mu.RLock()
	app := s.app
	s.mu.RUnlock()
	if app != nil {
		app.Event.Emit(projectManagerCodexStatusEventName, snapshot)
	}
}

func (snapshot CodexRuntimeStatusSnapshot) String() string {
	return fmt.Sprintf("sessions=%d projects=%d installed=%t", len(snapshot.Sessions), len(snapshot.Projects), snapshot.Monitor.Installed)
}
