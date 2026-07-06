package services

import (
	"fmt"
	"strings"
)

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

func projectManagerCodexProviderIDFromStore(store projectManagerStore, projectPath string) int64 {
	key := normalizeProjectManagerProjectPath(projectPath)
	if key == "" {
		return 0
	}
	meta, ok := store.Projects[key]
	if !ok {
		return 0
	}
	if meta.CodexProviderID <= 0 {
		return 0
	}
	return meta.CodexProviderID
}

func projectManagerCodexProviderIDForRequest(headers map[string]string, body []byte) int64 {
	projectPath := detectProjectManagerCodexProjectPath(headers, body)
	if projectPath == "" || projectPath == unknownProjectCaptureID {
		return 0
	}

	// relay 是高频请求路径，只读取项目绑定事实源，不在这里修复/迁移配置。
	// 如果读取失败直接回到全局链路，保证项目路由增强不会扩大为请求不可用故障。
	store, err := newProjectManagerStoreService().load()
	if err != nil {
		fmt.Printf("[ProjectProviderRouting] 读取项目 provider 绑定失败: %v\n", err)
		return 0
	}
	return projectManagerCodexProviderIDFromStore(store, projectPath)
}

func detectProjectManagerCodexProjectPath(headers map[string]string, body []byte) string {
	projectID, sessionID := DetectCaptureScope(headers, body)
	if projectID == unknownProjectCaptureID {
		if fallbackProjectID := detectCaptureProjectIDFromCodexSessionFiles(sessionID); fallbackProjectID != "" {
			projectID = fallbackProjectID
		}
	}
	return normalizeProjectManagerProjectPath(projectID)
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
