package services

import (
	"fmt"
	"strings"
)

type projectManagerCodexProviderRouting struct {
	ProviderID   int64
	AutoFallback bool
}

func resolveProjectManagerCodexProvider(store projectManagerStore, projectPath string) (int64, string) {
	providerID := projectManagerCodexProviderIDFromStore(store, projectPath)
	if providerID <= 0 {
		return 0, ""
	}

	// 快照只做展示增强，provider 缺失或配置读取失败时保留 ID。
	// 这样可以让用户看到“绑定仍存在但名称不可解析”，避免静默清掉事实源。
	providers, err := loadProviderSnapshot("codex")
	if err != nil {
		return providerID, ""
	}
	provider, ok := findProviderByID(providers, providerID)
	if !ok {
		return providerID, ""
	}
	return providerID, strings.TrimSpace(provider.Name)
}

func resolveProjectManagerCodexProviderAutoFallback(store projectManagerStore, projectPath string) bool {
	meta, ok := projectManagerProjectMetaFromStore(store, projectPath)
	if !ok || meta.CodexProviderID <= 0 {
		return true
	}
	return !meta.CodexProviderAutoFallbackDisabled
}

func projectManagerCodexProviderIDFromStore(store projectManagerStore, projectPath string) int64 {
	meta, ok := projectManagerProjectMetaFromStore(store, projectPath)
	if !ok {
		return 0
	}
	if meta.CodexProviderID <= 0 {
		return 0
	}
	return meta.CodexProviderID
}

func projectManagerCodexProviderIDForRequest(headers map[string]string, body []byte) int64 {
	return projectManagerCodexProviderRoutingForRequest(headers, body).ProviderID
}

func projectManagerCodexProviderRoutingForRequest(headers map[string]string, body []byte) projectManagerCodexProviderRouting {
	projectPath := detectProjectManagerCodexProjectPath(headers, body)
	if projectPath == "" || projectPath == unknownProjectCaptureID {
		return projectManagerCodexProviderRouting{AutoFallback: true}
	}

	// relay 是高频请求路径，只读取项目绑定事实源，不在这里修复/迁移配置。
	// 如果读取失败直接回到全局链路，保证项目路由增强不会扩大为请求不可用故障。
	store, err := newProjectManagerStoreService().load()
	if err != nil {
		fmt.Printf("[ProjectProviderRouting] 读取项目 provider 绑定失败: %v\n", err)
		return projectManagerCodexProviderRouting{AutoFallback: true}
	}
	providerID := projectManagerCodexProviderIDFromStore(store, projectPath)
	if providerID <= 0 {
		return projectManagerCodexProviderRouting{AutoFallback: true}
	}
	return projectManagerCodexProviderRouting{
		ProviderID:   providerID,
		AutoFallback: resolveProjectManagerCodexProviderAutoFallback(store, projectPath),
	}
}

func detectProjectManagerCodexProjectPath(headers map[string]string, _ []byte) string {
	normalizedHeaders := normalizeCaptureHeaders(headers)
	metadata := parseCodexTurnMetadata(normalizedHeaders["x-codex-turn-metadata"])

	// 路由身份只能来自 Codex 显式上下文。普通请求正文可能包含用户讨论的 cwd/project 文本，
	// 若复用日志采集的宽松扫描器，会把正文内容误当成真实项目并路由到错误 provider。
	projectID := detectFromNormalizedHeaders(projectHeaderKeys, normalizedHeaders)
	if projectID == "" {
		projectID = detectProjectFromCodexMetadata(metadata)
	}
	if projectID == "" {
		sessionID := detectFromNormalizedHeaders(sessionHeaderKeys, normalizedHeaders)
		if sessionID == "" {
			sessionID = strings.TrimSpace(metadata.SessionID)
		}
		if sessionID == "" {
			sessionID = strings.TrimSpace(metadata.ThreadID)
		}
		projectID = detectCaptureProjectIDFromCodexSessionFiles(sessionID)
	}
	return normalizeProjectManagerProjectPath(projectID)
}

func restrictToProjectPreferredProvider(active []Provider, preferredProviderID int64, autoFallback bool) []Provider {
	if preferredProviderID <= 0 || autoFallback {
		return active
	}
	for _, provider := range active {
		if provider.ID == preferredProviderID {
			return []Provider{provider}
		}
	}
	return nil
}

func prioritizeProjectPreferredProvider(active []Provider, preferredProviderID int64) []Provider {
	if preferredProviderID <= 0 || len(active) <= 1 {
		return active
	}

	// 只对已经通过 enabled、模型支持、黑名单等过滤的候选置顶。
	// 这是“首选 + 自动回落”，不是硬锁；首选不合格时不能绕过既有调度规则。
	preferredIndex := -1
	for index, provider := range active {
		if provider.ID == preferredProviderID {
			preferredIndex = index
			break
		}
	}
	if preferredIndex <= 0 {
		return active
	}

	ordered := make([]Provider, 0, len(active))
	ordered = append(ordered, active[preferredIndex])
	ordered = append(ordered, active[:preferredIndex]...)
	ordered = append(ordered, active[preferredIndex+1:]...)
	return ordered
}
