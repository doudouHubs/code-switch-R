package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// PetAgentToolProtocol 是 continuation 消息的协议方言。工具执行本身不依赖协议，
// 只有把 assistant call 和 tool result 送回 provider 时才需要这个信息。
type PetAgentToolProtocol string

const (
	PetAgentProtocolOpenAI    PetAgentToolProtocol = "openai"
	PetAgentProtocolAnthropic PetAgentToolProtocol = "anthropic"
	PetAgentProtocolGemini    PetAgentToolProtocol = "gemini"
)

const (
	PetAgentToolRead PetAgentToolName = "Read"
	PetAgentToolLS   PetAgentToolName = "LS"
	PetAgentToolGlob PetAgentToolName = "Glob"
	PetAgentToolGrep PetAgentToolName = "Grep"
)

type PetAgentToolName string

// PetAgentToolCall 是三个 provider 协议共同使用的 canonical tool call。
// Arguments 必须是 JSON object；绝不能把 JSON 字符串当作普通 assistant 文本。
type PetAgentToolCall struct {
	ID        string           `json:"id"`
	Name      PetAgentToolName `json:"name"`
	Arguments json.RawMessage  `json:"arguments"`
}

type PetAgentAssistantTurn struct {
	Text      string             `json:"text,omitempty"`
	ToolCalls []PetAgentToolCall `json:"toolCalls,omitempty"`
}

// PetAgentToolResult 是 native executor 的稳定结果。Content 保持文本，协议适配层
// 再把它放到 tool/tool_result/functionResponse 的原生字段中。
type PetAgentToolResult struct {
	ToolCallID string `json:"toolCallId"`
	ToolName   string `json:"toolName"`
	Content    string `json:"content"`
	IsError    bool   `json:"isError,omitempty"`
}

type PetAgentToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const (
	PetAgentToolErrorInvalidArguments = "invalid_arguments"
	PetAgentToolErrorUnknownTool      = "unknown_tool"
	PetAgentToolErrorPathOutsideRoot  = "path_outside_workspace"
	PetAgentToolErrorLimitExceeded    = "limit_exceeded"
	PetAgentToolErrorExecution        = "execution_error"
)

var (
	ErrPetAgentToolLoopMaxRounds      = errors.New("pet agent tool loop reached maximum rounds")
	ErrPetAgentToolLoopMaxCalls       = errors.New("pet agent tool loop reached maximum tool calls")
	ErrPetAgentToolLoopNoContinuation = errors.New("pet agent tool loop continuation callback is nil")
	ErrPetAgentToolWorkspaceInvalid   = errors.New("pet agent tool workspace is invalid")
	errPetAgentFileLimit              = errors.New("pet agent tool file limit")
)

// PetAgentToolDefinition 对齐 renderer/native worker 的 tool schema。返回值是新 slice，
// 调用方可以安全地把它序列化进请求而不会拿到 executor 的内部状态。
type PetAgentToolDefinition struct {
	Name        PetAgentToolName `json:"name"`
	Description string           `json:"description"`
	InputSchema map[string]any   `json:"inputSchema"`
}

func PetAgentToolDefinitions() []PetAgentToolDefinition {
	return []PetAgentToolDefinition{
		{
			Name:        PetAgentToolRead,
			Description: "Read a text file. Returns the file content with line numbers.",
			InputSchema: objectSchema(map[string]any{
				"file_path": stringProperty("Absolute path or relative to the workspace"),
				"offset":    numberProperty("1-based line number to start reading from"),
				"limit":     numberProperty("Maximum number of lines to read"),
			}, "file_path"),
		},
		{
			Name:        PetAgentToolLS,
			Description: "List files and directories in a workspace directory.",
			InputSchema: objectSchema(map[string]any{
				"path": stringProperty("Absolute path or relative to the workspace"),
			}, ""),
		},
		{
			Name:        PetAgentToolGlob,
			Description: "Find files matching a glob pattern.",
			InputSchema: objectSchema(map[string]any{
				"pattern": stringProperty("Glob pattern, for example src/**/*.go"),
				"path":    stringProperty("Optional search directory"),
				"limit":   numberProperty("Maximum result count"),
			}, "pattern"),
		},
		{
			Name:        PetAgentToolGrep,
			Description: "Search file contents using a regular expression.",
			InputSchema: objectSchema(map[string]any{
				"pattern":     stringProperty("Regular expression to search for"),
				"path":        stringProperty("File or directory to search"),
				"glob":        stringProperty("File glob to include"),
				"output_mode": stringProperty("matches, files_with_matches, files_without_matches, or count"),
				"maxResults":  numberProperty("Maximum result rows"),
				"maxDepth":    numberProperty("Maximum directory depth"),
				"ignoreCase":  boolProperty("Use case-insensitive matching"),
				"literal":     boolProperty("Treat pattern as a literal string"),
			}, "pattern"),
		},
	}
}

func objectSchema(properties map[string]any, required string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if required != "" {
		schema["required"] = []string{required}
	}
	return schema
}

func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func numberProperty(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func boolProperty(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

// PetAgentToolLimits 同时约束磁盘读取和返回给模型的内容，避免只限制文件大小却
// 仍然因为匹配数量或行数把上下文撑爆。
type PetAgentToolLimits struct {
	MaxFileBytes   int64
	MaxResultBytes int64
	MaxResults     int
	MaxReadLines   int
	MaxGlobDepth   int
	MaxGrepDepth   int
	MaxArguments   int64
}

func DefaultPetAgentToolLimits() PetAgentToolLimits {
	return PetAgentToolLimits{
		MaxFileBytes:   1 << 20,
		MaxResultBytes: 256 << 10,
		MaxResults:     2000,
		MaxReadLines:   4000,
		MaxGlobDepth:   32,
		MaxGrepDepth:   32,
		MaxArguments:   64 << 10,
	}
}

// PetAgentToolExecutor 只持有 canonical workspace 路径和限制，不持有任何写能力。
type PetAgentToolExecutor struct {
	root   string
	limits PetAgentToolLimits
}

func NewPetAgentToolExecutor(workspace string) (*PetAgentToolExecutor, error) {
	return NewPetAgentToolExecutorWithLimits(workspace, DefaultPetAgentToolLimits())
}

func NewPetAgentToolExecutorWithLimits(workspace string, limits PetAgentToolLimits) (*PetAgentToolExecutor, error) {
	root, err := filepath.Abs(strings.TrimSpace(workspace))
	if err != nil || strings.TrimSpace(workspace) == "" {
		return nil, ErrPetAgentToolWorkspaceInvalid
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, ErrPetAgentToolWorkspaceInvalid
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, ErrPetAgentToolWorkspaceInvalid
	}
	if limits.MaxFileBytes <= 0 || limits.MaxResultBytes <= 0 || limits.MaxResults <= 0 || limits.MaxReadLines <= 0 || limits.MaxGlobDepth <= 0 || limits.MaxGrepDepth <= 0 || limits.MaxArguments <= 0 {
		return nil, ErrPetAgentToolWorkspaceInvalid
	}
	return &PetAgentToolExecutor{root: filepath.Clean(root), limits: limits}, nil
}

func (e *PetAgentToolExecutor) WorkspaceRoot() string {
	if e == nil {
		return ""
	}
	return e.root
}

// Execute 执行一次 native tool call。参数错误、未知工具和安全边界错误都返回
// isError=true 的 tool result，模型可以据此修正调用；context 取消则直接终止 loop。
func (e *PetAgentToolExecutor) Execute(ctx context.Context, call PetAgentToolCall) (PetAgentToolResult, error) {
	result := PetAgentToolResult{ToolCallID: call.ID, ToolName: string(call.Name)}
	if err := contextError(ctx); err != nil {
		return result, err
	}
	if e == nil {
		return toolErrorResult(result, PetAgentToolErrorExecution, "tool executor is nil"), nil
	}
	if int64(len(call.Arguments)) > e.limits.MaxArguments {
		return toolErrorResult(result, PetAgentToolErrorLimitExceeded, "tool arguments exceed the limit"), nil
	}
	args, err := decodeToolArguments(call.Arguments)
	if err != nil {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	if call.ID == "" {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, "tool call id is required"), nil
	}

	switch call.Name {
	case PetAgentToolRead:
		return e.executeRead(ctx, result, args)
	case PetAgentToolLS:
		return e.executeLS(ctx, result, args)
	case PetAgentToolGlob:
		return e.executeGlob(ctx, result, args)
	case PetAgentToolGrep:
		return e.executeGrep(ctx, result, args)
	default:
		return toolErrorResult(result, PetAgentToolErrorUnknownTool, "unsupported read-only tool"), nil
	}
}

func (e *PetAgentToolExecutor) executeRead(ctx context.Context, result PetAgentToolResult, args map[string]json.RawMessage) (PetAgentToolResult, error) {
	if err := rejectUnknownArgs(args, "file_path", "offset", "limit"); err != nil {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	filePath, err := requiredString(args, "file_path")
	if err != nil {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	offset, err := optionalInt(args, "offset", 1)
	if err != nil || offset < 1 {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, "offset must be a positive integer"), nil
	}
	limit, err := optionalInt(args, "limit", e.limits.MaxReadLines)
	if err != nil || limit < 1 || limit > e.limits.MaxReadLines {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, "limit exceeds the read line limit"), nil
	}
	path, err := e.resolveExisting(filePath)
	if err != nil {
		return toolErrorResult(result, PetAgentToolErrorPathOutsideRoot, safePathError(err)), nil
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, "file_path must point to a regular file"), nil
	}
	data, err := readLimitedFile(ctx, path, e.limits.MaxFileBytes)
	if err != nil {
		return toolErrorResult(result, errorCode(err), safePathError(err)), errIfContext(err)
	}
	lines := strings.Split(string(data), "\n")
	start := offset - 1
	if start >= len(lines) {
		return resultWithContent(result, ""), nil
	}
	end := start + limit
	if end > len(lines) {
		end = len(lines)
	}
	var builder strings.Builder
	for index := start; index < end; index++ {
		if err := contextError(ctx); err != nil {
			return result, err
		}
		line := strings.TrimSuffix(lines[index], "\r")
		fmt.Fprintf(&builder, "%d\t%s\n", index+1, line)
	}
	if int64(builder.Len()) > e.limits.MaxResultBytes {
		return toolErrorResult(result, PetAgentToolErrorLimitExceeded, "tool result exceeds the limit"), nil
	}
	return resultWithContent(result, builder.String()), nil
}

func (e *PetAgentToolExecutor) executeLS(ctx context.Context, result PetAgentToolResult, args map[string]json.RawMessage) (PetAgentToolResult, error) {
	if err := rejectUnknownArgs(args, "path"); err != nil {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	directory, err := optionalString(args, "path", "")
	if err != nil {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	path, err := e.resolveExisting(directory)
	if err != nil {
		return toolErrorResult(result, PetAgentToolErrorPathOutsideRoot, safePathError(err)), nil
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, "path must point to a directory"), nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return toolErrorResult(result, PetAgentToolErrorExecution, "cannot list directory"), nil
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := contextError(ctx); err != nil {
			return result, err
		}
		entryPath := filepath.Join(path, entry.Name())
		if entry.Type()&os.ModeSymlink != 0 {
			if _, err := e.resolveExisting(entryPath); err != nil {
				return toolErrorResult(result, PetAgentToolErrorPathOutsideRoot, "directory contains a symlink outside the workspace"), nil
			}
		}
		name := entry.Name()
		if entry.IsDir() {
			name += string(filepath.Separator)
		}
		lines = append(lines, name)
	}
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	if int64(len(content)) > e.limits.MaxResultBytes || len(lines) > e.limits.MaxResults {
		return toolErrorResult(result, PetAgentToolErrorLimitExceeded, "tool result exceeds the limit"), nil
	}
	return resultWithContent(result, content), nil
}

func (e *PetAgentToolExecutor) executeGlob(ctx context.Context, result PetAgentToolResult, args map[string]json.RawMessage) (PetAgentToolResult, error) {
	if err := rejectUnknownArgs(args, "pattern", "path", "limit"); err != nil {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	pattern, err := requiredString(args, "pattern")
	if err != nil || strings.TrimSpace(pattern) == "" || filepath.IsAbs(pattern) {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, "pattern must be a relative non-empty glob"), nil
	}
	limit, err := optionalInt(args, "limit", e.limits.MaxResults)
	if err != nil || limit < 1 || limit > e.limits.MaxResults {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, "limit exceeds the result limit"), nil
	}
	baseInput, err := optionalString(args, "path", "")
	if err != nil {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	base, err := e.resolveExisting(baseInput)
	if err != nil {
		return toolErrorResult(result, PetAgentToolErrorPathOutsideRoot, safePathError(err)), nil
	}
	baseInfo, err := os.Stat(base)
	if err != nil || !baseInfo.IsDir() {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, "path must point to a directory"), nil
	}
	matcher, err := compilePetGlob(pattern)
	if err != nil {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, "invalid glob pattern"), nil
	}
	results := make([]string, 0)
	err = e.walk(ctx, base, e.limits.MaxGlobDepth, func(path string, info os.FileInfo, depth int) error {
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		if matcher.Match(filepath.ToSlash(rel)) {
			results = append(results, filepath.ToSlash(filepath.Join(relativeToRoot(e.root, base), rel)))
			if len(results) > limit {
				return errPetAgentResultLimit
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errPetAgentResultLimit) {
			return toolErrorResult(result, PetAgentToolErrorLimitExceeded, "glob result count exceeds the limit"), nil
		}
		if err := contextError(ctx); err != nil {
			return result, err
		}
		return toolErrorResult(result, PetAgentToolErrorExecution, "cannot scan workspace"), nil
	}
	sort.Strings(results)
	content := strings.Join(results, "\n")
	if content != "" {
		content += "\n"
	}
	if int64(len(content)) > e.limits.MaxResultBytes {
		return toolErrorResult(result, PetAgentToolErrorLimitExceeded, "tool result exceeds the limit"), nil
	}
	return resultWithContent(result, content), nil
}

func (e *PetAgentToolExecutor) executeGrep(ctx context.Context, result PetAgentToolResult, args map[string]json.RawMessage) (PetAgentToolResult, error) {
	if err := rejectUnknownArgs(args, "pattern", "path", "glob", "output_mode", "maxResults", "maxDepth", "ignoreCase", "literal"); err != nil {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	pattern, err := requiredString(args, "pattern")
	if err != nil || pattern == "" {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, "pattern is required"), nil
	}
	literal, err := optionalBool(args, "literal", false)
	if err != nil {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	ignoreCase, err := optionalBool(args, "ignoreCase", false)
	if err != nil {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	if literal {
		pattern = regexp.QuoteMeta(pattern)
	}
	if ignoreCase {
		pattern = "(?i)" + pattern
	}
	matcher, err := regexp.Compile(pattern)
	if err != nil {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, "invalid regular expression"), nil
	}
	mode, err := optionalString(args, "output_mode", "matches")
	if err != nil || (mode != "matches" && mode != "files_with_matches" && mode != "files_without_matches" && mode != "count") {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, "unsupported output_mode"), nil
	}
	maxResults, err := optionalInt(args, "maxResults", e.limits.MaxResults)
	if err != nil || maxResults < 1 || maxResults > e.limits.MaxResults {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, "maxResults exceeds the result limit"), nil
	}
	maxDepth, err := optionalInt(args, "maxDepth", e.limits.MaxGrepDepth)
	if err != nil || maxDepth < 0 || maxDepth > e.limits.MaxGrepDepth {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, "maxDepth exceeds the search depth limit"), nil
	}
	searchInput, err := optionalString(args, "path", "")
	if err != nil {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	searchPath, err := e.resolveExisting(searchInput)
	if err != nil {
		return toolErrorResult(result, PetAgentToolErrorPathOutsideRoot, safePathError(err)), nil
	}
	searchInfo, err := os.Stat(searchPath)
	if err != nil {
		return toolErrorResult(result, PetAgentToolErrorExecution, "cannot inspect search path"), nil
	}
	globPattern, err := optionalString(args, "glob", "")
	if err != nil {
		return toolErrorResult(result, PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	var globMatcher *petGlobMatcher
	if globPattern != "" {
		if filepath.IsAbs(globPattern) {
			return toolErrorResult(result, PetAgentToolErrorInvalidArguments, "glob must be relative"), nil
		}
		globMatcher, err = compilePetGlob(globPattern)
		if err != nil {
			return toolErrorResult(result, PetAgentToolErrorInvalidArguments, "invalid file glob"), nil
		}
	}
	rows := make([]string, 0)
	matchedFiles := make(map[string]bool)
	counts := make(map[string]int)
	scannedFiles := make(map[string]struct{})
	scanFile := func(path string) error {
		if err := contextError(ctx); err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return nil
		}
		if globMatcher != nil {
			rel, relErr := filepath.Rel(searchPath, path)
			if relErr != nil || !globMatcher.Match(filepath.ToSlash(rel)) {
				return nil
			}
		}
		data, err := readLimitedFile(ctx, path, e.limits.MaxFileBytes)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(e.root, path)
		if err != nil {
			return err
		}
		displayPath := filepath.ToSlash(rel)
		scannedFiles[displayPath] = struct{}{}
		lines := strings.Split(string(data), "\n")
		for lineIndex, line := range lines {
			line = strings.TrimSuffix(line, "\r")
			if matcher.MatchString(line) {
				matchedFiles[displayPath] = true
				counts[displayPath]++
				if mode == "matches" {
					rows = append(rows, fmt.Sprintf("%s:%d:%s", displayPath, lineIndex+1, line))
					if len(rows) > maxResults {
						return errPetAgentResultLimit
					}
				}
			}
		}
		return nil
	}
	if searchInfo.IsDir() {
		err = e.walk(ctx, searchPath, maxDepth, func(path string, info os.FileInfo, _ int) error {
			if info.IsDir() {
				return nil
			}
			if info.Mode()&os.ModeSymlink != 0 {
				if _, err := e.resolveExisting(path); err != nil {
					return err
				}
			}
			return scanFile(path)
		})
	} else {
		err = scanFile(searchPath)
	}
	if err != nil {
		if errors.Is(err, errPetAgentResultLimit) {
			return toolErrorResult(result, PetAgentToolErrorLimitExceeded, "grep result count exceeds the limit"), nil
		}
		if err := contextError(ctx); err != nil {
			return result, err
		}
		return toolErrorResult(result, errorCode(err), safePathError(err)), errIfContext(err)
	}
	switch mode {
	case "files_with_matches":
		for path := range matchedFiles {
			rows = append(rows, path)
		}
	case "files_without_matches":
		for path := range scannedFiles {
			if !matchedFiles[path] {
				rows = append(rows, path)
			}
		}
	case "count":
		for path, count := range counts {
			rows = append(rows, fmt.Sprintf("%s:%d", path, count))
		}
	}
	sort.Strings(rows)
	if len(rows) > maxResults {
		return toolErrorResult(result, PetAgentToolErrorLimitExceeded, "grep result count exceeds the limit"), nil
	}
	content := strings.Join(rows, "\n")
	if content != "" {
		content += "\n"
	}
	if int64(len(content)) > e.limits.MaxResultBytes {
		return toolErrorResult(result, PetAgentToolErrorLimitExceeded, "tool result exceeds the limit"), nil
	}
	return resultWithContent(result, content), nil
}

// walk 使用 ReadDir 而不是 filepath.WalkDir 的默认行为，显式限制深度并在每个
// symlink 上重新做 root 校验，避免未来改动成跟随链接后引入越界读取。
func (e *PetAgentToolExecutor) walk(ctx context.Context, root string, maxDepth int, visit func(string, os.FileInfo, int) error) error {
	var walkDir func(string, int) error
	walkDir = func(directory string, depth int) error {
		if err := contextError(ctx); err != nil {
			return err
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := contextError(ctx); err != nil {
				return err
			}
			path := filepath.Join(directory, entry.Name())
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			nextDepth := depth + 1
			if entry.Type()&os.ModeSymlink != 0 {
				if _, err := e.resolveExisting(path); err != nil {
					return err
				}
			}
			if err := visit(path, info, nextDepth); err != nil {
				return err
			}
			if info.IsDir() && nextDepth < maxDepth {
				if err := walkDir(path, nextDepth); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walkDir(root, 0)
}

func (e *PetAgentToolExecutor) resolveExisting(input string) (string, error) {
	input = strings.TrimSpace(input)
	path := e.root
	if input != "" {
		if filepath.IsAbs(input) {
			path = filepath.Clean(input)
		} else {
			path = filepath.Join(e.root, input)
		}
	}
	if !isWithinRoot(e.root, path) {
		return "", fmt.Errorf("path is outside workspace")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("path cannot be resolved")
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil || !isWithinRoot(e.root, canonical) {
		return "", fmt.Errorf("path resolves outside workspace")
	}
	return filepath.Clean(canonical), nil
}

func isWithinRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(rel) || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func relativeToRoot(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return ""
	}
	return rel
}

var errPetAgentResultLimit = errors.New("pet agent tool result limit")

type petGlobMatcher struct{ re *regexp.Regexp }

func (m *petGlobMatcher) Match(value string) bool { return m != nil && m.re.MatchString(value) }

func compilePetGlob(pattern string) (*petGlobMatcher, error) {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	if pattern == "" || strings.HasPrefix(pattern, "/") {
		return nil, errors.New("invalid glob")
	}
	var builder strings.Builder
	builder.WriteString("^")
	for index := 0; index < len(pattern); {
		switch pattern[index] {
		case '*':
			if index+1 < len(pattern) && pattern[index+1] == '*' {
				index += 2
				if index < len(pattern) && pattern[index] == '/' {
					builder.WriteString("(?:.*/)?")
					index++
				} else {
					builder.WriteString(".*")
				}
			} else {
				builder.WriteString("[^/]*")
				index++
			}
		case '?':
			builder.WriteString("[^/]")
			index++
		case '[':
			end := strings.IndexByte(pattern[index+1:], ']')
			if end < 0 {
				return nil, errors.New("unterminated character class")
			}
			end += index + 1
			class := pattern[index : end+1]
			if strings.Contains(class, "\\") {
				return nil, errors.New("invalid character class")
			}
			builder.WriteString(class)
			index = end + 1
		default:
			builder.WriteString(regexp.QuoteMeta(pattern[index : index+1]))
			index++
		}
	}
	builder.WriteString("$")
	re, err := regexp.Compile(builder.String())
	if err != nil {
		return nil, err
	}
	return &petGlobMatcher{re: re}, nil
}

func readLimitedFile(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open file")
	}
	defer file.Close()
	reader := io.LimitReader(file, maxBytes+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("cannot read file")
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errPetAgentFileLimit
	}
	return data, nil
}

func decodeToolArguments(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var args map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(&args); err != nil || args == nil {
		return nil, errors.New("arguments must be a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("arguments must contain one JSON value")
	}
	return args, nil
}

func rejectUnknownArgs(args map[string]json.RawMessage, allowed ...string) error {
	set := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		set[name] = struct{}{}
	}
	for name := range args {
		if _, ok := set[name]; !ok {
			return fmt.Errorf("unknown argument %q", name)
		}
	}
	return nil
}

func requiredString(args map[string]json.RawMessage, name string) (string, error) {
	value, ok := args[name]
	if !ok {
		return "", fmt.Errorf("%s is required", name)
	}
	return decodeString(value, name)
}

func optionalString(args map[string]json.RawMessage, name, fallback string) (string, error) {
	value, ok := args[name]
	if !ok {
		return fallback, nil
	}
	return decodeString(value, name)
}

func decodeString(raw json.RawMessage, name string) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s must be a string", name)
	}
	return strings.TrimSpace(value), nil
}

func optionalInt(args map[string]json.RawMessage, name string, fallback int) (int, error) {
	value, ok := args[name]
	if !ok {
		return fallback, nil
	}
	var number int
	if err := json.Unmarshal(value, &number); err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return number, nil
}

func optionalBool(args map[string]json.RawMessage, name string, fallback bool) (bool, error) {
	value, ok := args[name]
	if !ok {
		return fallback, nil
	}
	var result bool
	if err := json.Unmarshal(value, &result); err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return result, nil
}

func resultWithContent(result PetAgentToolResult, content string) PetAgentToolResult {
	result.Content = content
	return result
}

func toolErrorResult(result PetAgentToolResult, code, message string) PetAgentToolResult {
	payload, _ := json.Marshal(PetAgentToolError{Code: code, Message: message})
	result.Content = string(payload)
	result.IsError = true
	return result
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func errIfContext(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

func errorCode(err error) string {
	if errors.Is(err, errPetAgentFileLimit) {
		return PetAgentToolErrorLimitExceeded
	}
	if errors.Is(err, errPetAgentResultLimit) {
		return PetAgentToolErrorLimitExceeded
	}
	return PetAgentToolErrorExecution
}

func safePathError(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "outside") || strings.Contains(err.Error(), "resolve") {
		return "path is outside the workspace"
	}
	return "path cannot be read"
}

// PetAgentContinuationRequest 保存 canonical 结构和已经序列化的 native messages。
// NativeMessages 是 provider 请求层直接追加到 messages/contents 的协议片段。
type PetAgentContinuationRequest struct {
	Protocol       PetAgentToolProtocol
	Assistant      PetAgentAssistantTurn
	ToolResults    []PetAgentToolResult
	NativeMessages json.RawMessage
}

func BuildPetAgentContinuationRequest(protocol PetAgentToolProtocol, assistant PetAgentAssistantTurn, results []PetAgentToolResult) (PetAgentContinuationRequest, error) {
	protocol = normalizePetAgentProtocol(protocol)
	if protocol == "" {
		return PetAgentContinuationRequest{}, errors.New("unsupported tool protocol")
	}
	if len(assistant.ToolCalls) == 0 || len(results) != len(assistant.ToolCalls) {
		return PetAgentContinuationRequest{}, errors.New("assistant tool calls and results must have the same non-zero length")
	}
	for index, call := range assistant.ToolCalls {
		if call.ID == "" || call.Name == "" {
			return PetAgentContinuationRequest{}, errors.New("tool call id and name are required")
		}
		if results[index].ToolCallID != call.ID {
			return PetAgentContinuationRequest{}, errors.New("tool result order or id does not match assistant calls")
		}
	}

	var messages any
	switch protocol {
	case PetAgentProtocolOpenAI:
		messages = buildOpenAIContinuationMessages(assistant, results)
	case PetAgentProtocolAnthropic:
		messages = buildAnthropicContinuationMessages(assistant, results)
	case PetAgentProtocolGemini:
		messages = buildGeminiContinuationMessages(assistant, results)
	}
	native, err := json.Marshal(messages)
	if err != nil {
		return PetAgentContinuationRequest{}, err
	}
	return PetAgentContinuationRequest{Protocol: protocol, Assistant: assistant, ToolResults: append([]PetAgentToolResult(nil), results...), NativeMessages: native}, nil
}

func normalizePetAgentProtocol(protocol PetAgentToolProtocol) PetAgentToolProtocol {
	switch strings.ToLower(strings.TrimSpace(string(protocol))) {
	case "openai", "openai-chat", "openai-compatible":
		return PetAgentProtocolOpenAI
	case "anthropic", "messages":
		return PetAgentProtocolAnthropic
	case "gemini", "google-gemini", "generate-content":
		return PetAgentProtocolGemini
	default:
		return ""
	}
}

func buildOpenAIContinuationMessages(assistant PetAgentAssistantTurn, results []PetAgentToolResult) []map[string]any {
	toolCalls := make([]map[string]any, 0, len(assistant.ToolCalls))
	for _, call := range assistant.ToolCalls {
		arguments := string(call.Arguments)
		if arguments == "" {
			arguments = "{}"
		}
		toolCalls = append(toolCalls, map[string]any{"id": call.ID, "type": "function", "function": map[string]any{"name": string(call.Name), "arguments": arguments}})
	}
	messages := []map[string]any{{"role": "assistant", "content": assistant.Text, "tool_calls": toolCalls}}
	for _, result := range results {
		messages = append(messages, map[string]any{"role": "tool", "tool_call_id": result.ToolCallID, "content": result.Content})
	}
	return messages
}

func buildAnthropicContinuationMessages(assistant PetAgentAssistantTurn, results []PetAgentToolResult) []map[string]any {
	blocks := make([]map[string]any, 0, len(assistant.ToolCalls)+1)
	if assistant.Text != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": assistant.Text})
	}
	for _, call := range assistant.ToolCalls {
		var input map[string]any
		if json.Unmarshal(call.Arguments, &input) != nil || input == nil {
			input = map[string]any{}
		}
		blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID, "name": string(call.Name), "input": input})
	}
	toolBlocks := make([]map[string]any, 0, len(results))
	for _, result := range results {
		block := map[string]any{"type": "tool_result", "tool_use_id": result.ToolCallID, "content": result.Content}
		if result.IsError {
			block["is_error"] = true
		}
		toolBlocks = append(toolBlocks, block)
	}
	return []map[string]any{{"role": "assistant", "content": blocks}, {"role": "user", "content": toolBlocks}}
}

func buildGeminiContinuationMessages(assistant PetAgentAssistantTurn, results []PetAgentToolResult) []map[string]any {
	modelParts := make([]map[string]any, 0, len(assistant.ToolCalls)+1)
	if assistant.Text != "" {
		modelParts = append(modelParts, map[string]any{"text": assistant.Text})
	}
	for _, call := range assistant.ToolCalls {
		var args map[string]any
		if json.Unmarshal(call.Arguments, &args) != nil || args == nil {
			args = map[string]any{}
		}
		modelParts = append(modelParts, map[string]any{"functionCall": map[string]any{"name": string(call.Name), "args": args}})
	}
	responseParts := make([]map[string]any, 0, len(results))
	for _, result := range results {
		responseParts = append(responseParts, map[string]any{"functionResponse": map[string]any{"name": result.ToolName, "response": map[string]any{"content": result.Content, "is_error": result.IsError}}})
	}
	return []map[string]any{{"role": "model", "parts": modelParts}, {"role": "user", "parts": responseParts}}
}

type PetAgentContinuationFunc func(context.Context, PetAgentContinuationRequest) (PetAgentAssistantTurn, error)

type PetAgentToolLoopOptions struct {
	Protocol     PetAgentToolProtocol
	MaxRounds    int
	MaxToolCalls int
}

type PetAgentToolLoopResult struct {
	Final         PetAgentAssistantTurn
	Rounds        int
	ToolCallCount int
	Continuations []PetAgentContinuationRequest
}

// PetAgentToolLoopCoordinator 将“assistant tool calls -> native executor -> native
// results -> provider continuation”固定成唯一主流程，避免不同 provider 各写一份循环。
type PetAgentToolLoopCoordinator struct {
	executor     *PetAgentToolExecutor
	continuation PetAgentContinuationFunc
	options      PetAgentToolLoopOptions
}

func NewPetAgentToolLoopCoordinator(executor *PetAgentToolExecutor, continuation PetAgentContinuationFunc, options PetAgentToolLoopOptions) *PetAgentToolLoopCoordinator {
	if options.MaxRounds <= 0 {
		options.MaxRounds = 8
	}
	if options.MaxToolCalls <= 0 {
		options.MaxToolCalls = 32
	}
	options.Protocol = normalizePetAgentProtocol(options.Protocol)
	return &PetAgentToolLoopCoordinator{executor: executor, continuation: continuation, options: options}
}

func (c *PetAgentToolLoopCoordinator) Run(ctx context.Context, initial PetAgentAssistantTurn) (PetAgentToolLoopResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.executor == nil {
		return PetAgentToolLoopResult{}, ErrPetAgentToolWorkspaceInvalid
	}
	if c.options.Protocol == "" {
		return PetAgentToolLoopResult{}, errors.New("unsupported tool protocol")
	}
	current := initial
	loopResult := PetAgentToolLoopResult{}
	for len(current.ToolCalls) > 0 {
		if err := contextError(ctx); err != nil {
			return loopResult, err
		}
		if loopResult.Rounds >= c.options.MaxRounds {
			return loopResult, ErrPetAgentToolLoopMaxRounds
		}
		if loopResult.ToolCallCount+len(current.ToolCalls) > c.options.MaxToolCalls {
			return loopResult, ErrPetAgentToolLoopMaxCalls
		}
		results := make([]PetAgentToolResult, 0, len(current.ToolCalls))
		for _, call := range current.ToolCalls {
			result, err := c.executor.Execute(ctx, call)
			if err != nil {
				return loopResult, err
			}
			results = append(results, result)
			loopResult.ToolCallCount++
		}
		request, err := BuildPetAgentContinuationRequest(c.options.Protocol, current, results)
		if err != nil {
			return loopResult, err
		}
		loopResult.Continuations = append(loopResult.Continuations, request)
		loopResult.Rounds++
		if c.continuation == nil {
			return loopResult, ErrPetAgentToolLoopNoContinuation
		}
		current, err = c.continuation(ctx, request)
		if err != nil {
			return loopResult, err
		}
	}
	loopResult.Final = current
	return loopResult, nil
}

// ParsePetAgentOpenAIArguments 把 OpenAI function.arguments 统一成 canonical JSON object。
func ParsePetAgentOpenAIArguments(raw string) (json.RawMessage, error) {
	args, err := decodeToolArguments(json.RawMessage(raw))
	if err != nil {
		return nil, err
	}
	return json.Marshal(args)
}

// ParsePetAgentToolCallArguments 是给 Anthropic/Gemini 归一化器使用的别名，确保
// 两类协议也在进入 coordinator 前经过同一套 object 校验。
func ParsePetAgentToolCallArguments(raw json.RawMessage) (json.RawMessage, error) {
	args, err := decodeToolArguments(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(args)
}
