package services

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const (
	requestCaptureDirName       = "request-captures"
	unknownProjectCaptureID     = "unknown-project"
	unknownSessionCaptureID     = "unknown-session"
	maxCapturePathSegmentLength = 80
	maxCaptureDetectionDepth    = 6
)

var (
	projectDetectionPaths = []string{
		"project",
		"project_id",
		"projectId",
		"workspace",
		"workspace_id",
		"workspaceId",
		"workspace_root",
		"workspaceRoot",
		"cwd",
		"root",
		"root_path",
		"rootPath",
		"project_root_path",
		"projectRootPath",
		"workdir",
		"working_directory",
		"workingDirectory",
	}
	sessionDetectionPaths = []string{
		"session",
		"session_id",
		"sessionId",
		"conversation_id",
		"conversationId",
		"thread_id",
		"threadId",
		"chat_id",
		"chatId",
		"request_id",
		"requestId",
	}
	projectHeaderKeys = []string{
		"x-project-id",
		"x-project",
		"x-project-root-path",
		"x-workdir",
		"x-workspace-id",
		"x-workspace-root",
		"x-cwd",
	}
	sessionHeaderKeys = []string{
		"x-session-id",
		"session-id",
		"x-conversation-id",
		"x-thread-id",
		"thread-id",
		"x-chat-id",
		"x-request-id",
		"x-client-request-id",
	}
	projectDetectionKeySet = newCaptureKeySet(projectDetectionPaths)
)

type codexTurnMetadata struct {
	SessionID  string                     `json:"session_id"`
	ThreadID   string                     `json:"thread_id"`
	Workspaces map[string]json.RawMessage `json:"workspaces"`
}

type RequestCaptureRequest struct {
	Method   string            `json:"method"`
	Endpoint string            `json:"endpoint"`
	Query    map[string]string `json:"query,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Body     any               `json:"body,omitempty"`
}

type RequestCaptureRecord struct {
	CapturedAt string                `json:"captured_at"`
	Platform   string                `json:"platform"`
	ProjectID  string                `json:"project_id"`
	SessionID  string                `json:"session_id"`
	Request    RequestCaptureRequest `json:"request"`
}

type RequestCaptureContext struct {
	Platform string
	Method   string
	Endpoint string
	Query    map[string]string
	Headers  map[string]string
	Body     []byte
}

type RequestCaptureService struct {
	appSettings *AppSettingsService
}

func NewRequestCaptureService(appSettings *AppSettingsService) *RequestCaptureService {
	return &RequestCaptureService{
		appSettings: appSettings,
	}
}

func (s *RequestCaptureService) Capture(ctx RequestCaptureContext) error {
	if s == nil || !s.Enabled() {
		return nil
	}

	projectID, sessionID := DetectCaptureScope(ctx.Headers, ctx.Body)
	record := RequestCaptureRecord{
		CapturedAt: time.Now().Format(time.RFC3339Nano),
		Platform:   strings.TrimSpace(ctx.Platform),
		ProjectID:  projectID,
		SessionID:  sessionID,
		Request: RequestCaptureRequest{
			Method:   strings.TrimSpace(ctx.Method),
			Endpoint: strings.TrimSpace(ctx.Endpoint),
			Query:    cloneCaptureStringMap(ctx.Query),
			Headers:  cloneCaptureStringMap(ctx.Headers),
			Body:     parseCaptureBody(ctx.Body),
		},
	}

	baseDir, err := s.resolveBaseDir()
	if err != nil {
		return fmt.Errorf("解析请求捕获目录失败: %w", err)
	}
	dir := filepath.Join(
		baseDir,
		sanitizeCapturePathSegment(record.Platform, "unknown-platform"),
		sanitizeCapturePathSegment(record.ProjectID, unknownProjectCaptureID),
		sanitizeCapturePathSegment(record.SessionID, unknownSessionCaptureID),
	)
	filename := fmt.Sprintf("%s-%s.json", time.Now().Format("20060102-150405.000000000"), randomCaptureID())
	path := filepath.Join(dir, filename)
	if err := AtomicWriteJSON(path, record); err != nil {
		return fmt.Errorf("写入请求捕获失败: %w", err)
	}
	return nil
}

func (s *RequestCaptureService) Enabled() bool {
	if s == nil || s.appSettings == nil {
		return true
	}
	settings, err := s.appSettings.GetAppSettings()
	if err != nil {
		return true
	}
	return settings.EnableRequestCapture
}

func (s *RequestCaptureService) BaseDir() string {
	baseDir, err := s.resolveBaseDir()
	if s == nil || err != nil {
		return ""
	}
	return baseDir
}

func (s *RequestCaptureService) resolveBaseDir() (string, error) {
	defaultDir := defaultRequestCaptureBaseDir()
	if s == nil || s.appSettings == nil {
		return defaultDir, nil
	}

	settings, err := s.appSettings.GetAppSettings()
	if err != nil {
		return defaultDir, nil
	}

	configuredDir, err := normalizeRequestCaptureDir(settings.RequestCaptureDir)
	if err != nil {
		return "", err
	}
	if configuredDir != "" {
		return configuredDir, nil
	}
	return defaultDir, nil
}

func defaultRequestCaptureBaseDir() string {
	home, err := getUserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, appSettingsDir, requestCaptureDirName)
}

func normalizeRequestCaptureDir(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	if !filepath.IsAbs(trimmed) {
		abs, err := filepath.Abs(trimmed)
		if err != nil {
			return "", err
		}
		trimmed = abs
	}
	cleaned := filepath.Clean(trimmed)
	if strings.TrimSpace(cleaned) == "" {
		return "", nil
	}
	return cleaned, nil
}

func NormalizeRequestCaptureDirForSettings(value string) (string, error) {
	return normalizeRequestCaptureDir(value)
}

func DetectCaptureScope(headers map[string]string, body []byte) (projectID string, sessionID string) {
	normalizedHeaders := normalizeCaptureHeaders(headers)
	metadata := parseCodexTurnMetadata(normalizedHeaders["x-codex-turn-metadata"])

	projectID = detectCaptureProjectID(normalizedHeaders, metadata, body)
	if projectID == "" {
		projectID = unknownProjectCaptureID
	}

	sessionID = detectCaptureSessionID(normalizedHeaders, metadata, body)
	if sessionID == "" {
		sessionID = unknownSessionCaptureID
	}

	return projectID, sessionID
}

func detectCaptureProjectID(headers map[string]string, metadata codexTurnMetadata, body []byte) string {
	if value := detectFromJSONPaths(projectDetectionPaths, body); value != "" {
		return value
	}
	if value := detectFromNormalizedHeaders(projectHeaderKeys, headers); value != "" {
		return value
	}
	if value := detectProjectFromCodexMetadata(metadata); value != "" {
		return value
	}
	if value := detectFromStructuredCaptureBody(body); value != "" {
		return value
	}
	return detectProjectFromCaptureText(body)
}

func detectCaptureSessionID(headers map[string]string, metadata codexTurnMetadata, body []byte) string {
	if value := detectFromJSONPaths(sessionDetectionPaths, body); value != "" {
		return value
	}
	if value := detectFromNormalizedHeaders(sessionHeaderKeys, headers); value != "" {
		return value
	}
	if value := strings.TrimSpace(metadata.SessionID); value != "" {
		return value
	}
	if value := strings.TrimSpace(metadata.ThreadID); value != "" {
		return value
	}
	if value := detectFromStructuredCaptureBodyByKeys(body, newCaptureKeySet(sessionDetectionPaths)); value != "" {
		return value
	}
	return ""
}

func detectFromJSONPaths(paths []string, body []byte) string {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ""
	}
	for _, path := range paths {
		value := strings.TrimSpace(gjson.GetBytes(body, path).String())
		if value != "" {
			return value
		}
	}
	return ""
}

func detectFromNormalizedHeaders(keys []string, headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	for _, key := range keys {
		if value := headers[strings.ToLower(strings.TrimSpace(key))]; value != "" {
			return value
		}
	}
	return ""
}

func normalizeCaptureHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return map[string]string{}
	}
	normalized := make(map[string]string, len(headers))
	for key, value := range headers {
		normalized[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return normalized
}

func parseCodexTurnMetadata(raw string) codexTurnMetadata {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return codexTurnMetadata{}
	}
	var metadata codexTurnMetadata
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return codexTurnMetadata{}
	}
	return metadata
}

func detectProjectFromCodexMetadata(metadata codexTurnMetadata) string {
	if len(metadata.Workspaces) == 0 {
		return ""
	}
	keys := make([]string, 0, len(metadata.Workspaces))
	for key := range metadata.Workspaces {
		key = strings.TrimSpace(key)
		if key != "" {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	return keys[0]
}

func detectFromStructuredCaptureBody(body []byte) string {
	return detectFromStructuredCaptureBodyByKeys(body, projectDetectionKeySet)
}

func detectFromStructuredCaptureBodyByKeys(body []byte, keySet map[string]struct{}) string {
	if len(body) == 0 || !json.Valid(body) {
		return ""
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return detectFromStructuredCaptureValueByKeys(payload, keySet, 0)
}

func detectFromStructuredCaptureValueByKeys(value any, keySet map[string]struct{}, depth int) string {
	if depth > maxCaptureDetectionDepth {
		return ""
	}

	switch typed := value.(type) {
	case map[string]any:
		if value := detectFromCaptureMapByKeys(typed, keySet); value != "" {
			return value
		}
		for _, key := range sortedCaptureMapKeys(typed) {
			if value := detectFromStructuredCaptureValueByKeys(typed[key], keySet, depth+1); value != "" {
				return value
			}
		}
	case []any:
		for _, item := range typed {
			if value := detectFromStructuredCaptureValueByKeys(item, keySet, depth+1); value != "" {
				return value
			}
		}
	case string:
		if nested, ok := parseCaptureNestedJSON(typed); ok {
			return detectFromStructuredCaptureValueByKeys(nested, keySet, depth+1)
		}
	}

	return ""
}

func detectFromCaptureMapByKeys(values map[string]any, keySet map[string]struct{}) string {
	for _, key := range sortedCaptureMapKeys(values) {
		if _, ok := keySet[normalizeCaptureKey(key)]; !ok {
			continue
		}
		if value, ok := values[key].(string); ok {
			value = strings.TrimSpace(value)
			if value != "" {
				return value
			}
		}
	}
	return ""
}

func detectProjectFromCaptureText(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if !json.Valid(body) {
		return detectProjectFromCaptureString(string(body))
	}

	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return detectProjectFromCaptureString(string(body))
	}
	return detectProjectFromCaptureValueText(payload, 0)
}

func detectProjectFromCaptureValueText(value any, depth int) string {
	if depth > maxCaptureDetectionDepth {
		return ""
	}

	switch typed := value.(type) {
	case map[string]any:
		for _, key := range sortedCaptureMapKeys(typed) {
			if value := detectProjectFromCaptureValueText(typed[key], depth+1); value != "" {
				return value
			}
		}
	case []any:
		for _, item := range typed {
			if value := detectProjectFromCaptureValueText(item, depth+1); value != "" {
				return value
			}
		}
	case string:
		return detectProjectFromCaptureString(typed)
	}

	return ""
}

func detectProjectFromCaptureString(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if root := extractCaptureTagValue(value, "root"); root != "" {
		return root
	}
	return extractCaptureTagValue(value, "cwd")
}

func extractCaptureTagValue(content string, tag string) string {
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

func parseCaptureNestedJSON(content string) (any, bool) {
	content = strings.TrimSpace(content)
	if len(content) < 2 {
		return nil, false
	}
	if !(strings.HasPrefix(content, "{") && strings.HasSuffix(content, "}")) &&
		!(strings.HasPrefix(content, "[") && strings.HasSuffix(content, "]")) {
		return nil, false
	}
	var payload any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return nil, false
	}
	return payload, true
}

func newCaptureKeySet(values []string) map[string]struct{} {
	keySet := make(map[string]struct{}, len(values))
	for _, value := range values {
		if normalized := normalizeCaptureKey(value); normalized != "" {
			keySet[normalized] = struct{}{}
		}
	}
	return keySet
}

func normalizeCaptureKey(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			return r
		}
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, value)
}

func sortedCaptureMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sanitizeCapturePathSegment(value string, fallback string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fallback
	}

	replacer := strings.NewReplacer(
		"<", "_",
		">", "_",
		":", "_",
		"\"", "_",
		"/", "_",
		"\\", "_",
		"|", "_",
		"?", "_",
		"*", "_",
	)
	sanitized := replacer.Replace(trimmed)
	sanitized = strings.Map(func(r rune) rune {
		if r < 32 {
			return '_'
		}
		return r
	}, sanitized)
	sanitized = strings.TrimSpace(strings.Trim(sanitized, ". "))
	if sanitized == "" {
		return fallback
	}
	runes := []rune(sanitized)
	if len(runes) > maxCapturePathSegmentLength {
		sanitized = string(runes[:maxCapturePathSegmentLength])
	}
	return sanitized
}

func parseCaptureBody(body []byte) any {
	if len(body) == 0 {
		return nil
	}
	if json.Valid(body) {
		var payload any
		if err := json.Unmarshal(body, &payload); err == nil {
			return payload
		}
	}
	return string(body)
}

func cloneCaptureStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return map[string]string{}
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	cloned := make(map[string]string, len(input))
	for _, key := range keys {
		cloned[key] = input[key]
	}
	return cloned
}

func randomCaptureID() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
