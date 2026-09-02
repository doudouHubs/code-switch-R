package channels

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"codeswitch/services"
)

// channelCodexDynamicToolProvider 是频道 Agent 的工具 owner。Codex runtime
// 只负责注册/调用协议；实例能力、项目 session 和权限校验全部在这里收口。
type channelCodexDynamicToolProvider struct {
	store     *Store
	manager   *Manager
	eventSink EventSink
}

func newChannelCodexDynamicToolProvider(store *Store, manager *Manager, eventSink EventSink) *channelCodexDynamicToolProvider {
	return &channelCodexDynamicToolProvider{store: store, manager: manager, eventSink: eventSink}
}

// NewChannelCodexDynamicToolProvider 暴露频道工具 provider 的构造入口；工具
// provider 仍由 channels 包拥有，main.go 只注入它，不复制任何工具定义或权限逻辑。
func NewChannelCodexDynamicToolProvider(store *Store, manager *Manager, eventSink EventSink) services.PetCodexDynamicToolProvider {
	return newChannelCodexDynamicToolProvider(store, manager, eventSink)
}

func (p *channelCodexDynamicToolProvider) Snapshot(scope string) (services.PetCodexDynamicToolSnapshot, error) {
	projectID, err := parseChannelProjectToolScope(scope)
	if err != nil {
		return services.PetCodexDynamicToolSnapshot{}, err
	}
	if p == nil || p.store == nil {
		return services.PetCodexDynamicToolSnapshot{}, errors.New("channel store is unavailable")
	}
	instances, err := p.store.ListInstances()
	if err != nil {
		return services.PetCodexDynamicToolSnapshot{}, err
	}
	// 一个项目可能绑定多个平台；thread 的工具 schema 必须是项目级稳定快照，
	// 不能使用当前入站频道的 session/chat，否则同项目切换入口就会触发新 thread。
	definitions, descriptors := projectChannelToolDefinitions(projectID, instances)
	return services.PetCodexDynamicToolSnapshot{
		Definitions: definitions,
		Fingerprint: channelProjectToolFingerprint(projectID, descriptors, definitions),
	}, nil
}

func (p *channelCodexDynamicToolProvider) NewExecutor(ctx context.Context, scope, workspace string) (services.PetAgentToolRunner, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	instance, session, err := p.resolveScope(scope)
	if err != nil {
		return nil, err
	}
	if !instance.Enabled {
		return nil, errors.New("channel is disabled")
	}
	resolvedWorkspace, err := normalizeWorkspace(ctx, workspace)
	if err != nil {
		return nil, err
	}
	sessionWorkspace, err := normalizeWorkspace(ctx, session.WorkingFolder)
	if err != nil || !sameChannelWorkspace(resolvedWorkspace, sessionWorkspace) {
		return nil, errors.New("channel Codex workspace does not match the session")
	}
	return newChannelAgentToolExecutor(
		p.store,
		p.manager,
		p.eventSink,
		instance,
		session.ID,
		session.ChatID,
		resolvedWorkspace,
	)
}

func (p *channelCodexDynamicToolProvider) resolveScope(scope string) (ChannelInstance, ChannelSession, error) {
	instanceID, sessionID, chatID, err := parseChannelToolScope(scope)
	if err != nil {
		return ChannelInstance{}, ChannelSession{}, err
	}
	if p == nil || p.store == nil {
		return ChannelInstance{}, ChannelSession{}, errors.New("channel store is unavailable")
	}
	instance, found, err := p.store.GetInstance(instanceID)
	if err != nil {
		return ChannelInstance{}, ChannelSession{}, err
	}
	if !found {
		return ChannelInstance{}, ChannelSession{}, errors.New("channel instance not found")
	}
	session, found, err := p.store.GetSessionByID(sessionID)
	if err != nil {
		return ChannelInstance{}, ChannelSession{}, err
	}
	if !found || session.InstanceID != instanceID || session.ChatID != chatID {
		return ChannelInstance{}, ChannelSession{}, errors.New("channel tool session scope mismatch")
	}
	return instance, session, nil
}

func parseChannelProjectToolScope(scope string) (string, error) {
	scope = strings.TrimSpace(scope)
	if !strings.HasPrefix(scope, services.PetCodexProjectToolScope("")) {
		return "", errors.New("channel project tool scope is invalid")
	}
	projectID := strings.TrimSpace(strings.TrimPrefix(scope, services.PetCodexProjectToolScope("")))
	if projectID == "" {
		return "", errors.New("channel project tool scope is missing project id")
	}
	return projectID, nil
}

type channelProjectToolDescriptor struct {
	ID          string             `json:"id"`
	Type        string             `json:"type"`
	Tools       map[string]bool    `json:"tools"`
	Permissions ChannelPermissions `json:"permissions"`
}

func projectChannelToolDefinitions(projectID string, instances []ChannelInstance) ([]services.PetAgentToolDefinition, []channelProjectToolDescriptor) {
	definitionsByName := make(map[services.PetAgentToolName]services.PetAgentToolDefinition)
	descriptors := make([]channelProjectToolDescriptor, 0)
	for _, instance := range instances {
		if !instance.Enabled || instance.ProjectID == nil || strings.TrimSpace(*instance.ProjectID) != projectID {
			continue
		}
		permissions := instance.Permissions
		permissions.ReadablePathPrefixes = append([]string(nil), permissions.ReadablePathPrefixes...)
		sort.Strings(permissions.ReadablePathPrefixes)
		descriptors = append(descriptors, channelProjectToolDescriptor{
			ID: instance.ID, Type: instance.Type, Tools: instance.Tools, Permissions: permissions,
		})
		for _, definition := range channelToolDefinitionsForInstance(instance) {
			if _, exists := definitionsByName[definition.Name]; !exists {
				definitionsByName[definition.Name] = definition
			}
		}
	}
	sort.Slice(descriptors, func(i, j int) bool {
		if descriptors[i].ID != descriptors[j].ID {
			return descriptors[i].ID < descriptors[j].ID
		}
		return descriptors[i].Type < descriptors[j].Type
	})
	definitions := make([]services.PetAgentToolDefinition, 0, len(definitionsByName))
	for _, definition := range definitionsByName {
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	return definitions, descriptors
}

func channelProjectToolFingerprint(projectID string, descriptors []channelProjectToolDescriptor, definitions []services.PetAgentToolDefinition) string {
	payload := struct {
		ProjectID   string                            `json:"projectId"`
		Instances   []channelProjectToolDescriptor    `json:"instances"`
		Definitions []services.PetAgentToolDefinition `json:"definitions"`
	}{
		ProjectID:   projectID,
		Instances:   descriptors,
		Definitions: definitions,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		// 当前字段均为 JSON 原生类型；保留固定值作为极端兜底，不能让一次
		// schema 编码异常把 runtime 误认为拥有可复用的旧权限上下文。
		return "channel-tools-invalid"
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func sameChannelWorkspace(left, right string) bool {
	left = filepath.Clean(strings.TrimSpace(left))
	right = filepath.Clean(strings.TrimSpace(right))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

var _ services.PetCodexDynamicToolProvider = (*channelCodexDynamicToolProvider)(nil)
