package channels

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

type Manager struct {
	store     *Store
	factories map[string]ProviderFactory
	providers map[string]ChannelProvider
	statuses  map[string]ChannelStatus
	eventSink EventSink
	mu        sync.RWMutex
}

func NewManager(store *Store, eventSink EventSink) *Manager {
	manager := &Manager{store: store, factories: make(map[string]ProviderFactory), providers: make(map[string]ChannelProvider), statuses: make(map[string]ChannelStatus), eventSink: eventSink}
	RegisterBuiltinFactories(manager)
	return manager
}

func (m *Manager) RegisterFactory(channelType string, factory ProviderFactory) {
	if m == nil || strings.TrimSpace(channelType) == "" || factory == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.factories[channelType] = factory
}

func (m *Manager) setStatus(instance ChannelInstance, state, errorText string) {
	status := ChannelStatus{InstanceID: instance.ID, State: state, Error: errorText, UpdatedAt: nowMillis()}
	m.mu.Lock()
	m.statuses[instance.ID] = status
	m.mu.Unlock()
	instance.Status = state
	instance.LastError = errorText
	instance.UpdatedAt = status.UpdatedAt
	if m.store != nil {
		if err := m.store.UpsertInstance(instance); err != nil && m.eventSink != nil {
			m.eventSink(ChannelEvent{Type: "error", InstanceID: instance.ID, PluginType: instance.Type, Data: err.Error(), At: nowMillis()})
		}
	}
	if m.eventSink != nil {
		m.eventSink(ChannelEvent{Type: "status_change", InstanceID: instance.ID, PluginType: instance.Type, Data: status, At: nowMillis()})
	}
}

func (m *Manager) Start(ctx context.Context, id string) error {
	if m == nil || m.store == nil {
		return errors.New("channel manager is unavailable")
	}
	instance, found, err := m.store.GetInstance(id)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("channel instance %q not found", id)
	}
	if instance.ProjectID == nil || strings.TrimSpace(*instance.ProjectID) == "" {
		return errors.New("channel must be bound to a project before it can start")
	}
	if !instance.Enabled {
		return errors.New("channel is disabled")
	}
	m.mu.RLock()
	factory := m.factories[instance.Type]
	existing := m.providers[instance.ID]
	m.mu.RUnlock()
	if factory == nil {
		return fmt.Errorf("no channel provider registered for %s", instance.Type)
	}
	if existing != nil {
		_ = m.Stop(ctx, instance.ID)
	}
	provider, err := factory(instance, func(event ChannelEvent) {
		if m.eventSink != nil {
			m.eventSink(event)
		}
	})
	if err != nil {
		m.setStatus(instance, "error", err.Error())
		return err
	}
	m.mu.Lock()
	m.providers[instance.ID] = provider
	m.mu.Unlock()
	if err := provider.Start(ctx); err != nil {
		m.mu.Lock()
		delete(m.providers, instance.ID)
		m.mu.Unlock()
		m.setStatus(instance, "error", err.Error())
		return err
	}
	m.setStatus(instance, "running", "")
	return nil
}

func (m *Manager) Stop(ctx context.Context, id string) error {
	if m == nil || m.store == nil {
		return errors.New("channel manager is unavailable")
	}
	instance, found, err := m.store.GetInstance(id)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	m.mu.Lock()
	provider := m.providers[id]
	delete(m.providers, id)
	m.mu.Unlock()
	if provider != nil {
		if err := stopChannelProvider(ctx, provider); err != nil {
			m.setStatus(instance, "error", err.Error())
			return err
		}
	}
	m.setStatus(instance, "stopped", "")
	return nil
}

func stopChannelProvider(ctx context.Context, provider ChannelProvider) error {
	if provider == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	finished := make(chan error, 1)
	go func() {
		finished <- provider.Stop(ctx)
	}()
	select {
	case err := <-finished:
		return err
	case <-ctx.Done():
		// 旧平台 provider 的 Stop 实现可能在内部 Wait 长轮询；不能把
		// 这个不可取消的实现带进 Wails shutdown 调用栈，超时后由上层继续
		// 收口。goroutine 使用的 provider 已经从 Manager 中摘除，不再接收新请求。
		return ctx.Err()
	}
}

func (m *Manager) StartAuto(ctx context.Context) ([]string, []string, error) {
	instances, err := m.store.ListInstances()
	if err != nil {
		return nil, nil, err
	}
	started, failed := make([]string, 0), make([]string, 0)
	for _, instance := range instances {
		if !instance.Enabled || !instance.Features.AutoStart || instance.ProjectID == nil || strings.TrimSpace(*instance.ProjectID) == "" {
			continue
		}
		if err := m.Start(ctx, instance.ID); err != nil {
			failed = append(failed, instance.ID)
		} else {
			started = append(started, instance.ID)
		}
	}
	return started, failed, nil
}

func (m *Manager) StopAll(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	ids := make([]string, 0, len(m.providers))
	for id := range m.providers {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	if len(ids) == 0 {
		return nil
	}
	results := make(chan error, len(ids))
	for _, id := range ids {
		go func(id string) {
			results <- m.Stop(ctx, id)
		}(id)
	}
	var firstErr error
	for completed := 0; completed < len(ids); completed++ {
		select {
		case err := <-results:
			if err != nil && firstErr == nil {
				firstErr = err
			}
		case <-ctx.Done():
			// Stop 的单 provider 包装已经把不可取消的 Wait 隔离到后台；
			// StopAll 不再因为某一个平台连接拖住整个应用退出。
			return ctx.Err()
		}
	}
	return firstErr
}

func (m *Manager) Status(id string) ChannelStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if status, ok := m.statuses[id]; ok {
		return status
	}
	return ChannelStatus{InstanceID: id, State: "stopped"}
}
func (m *Manager) Statuses() []ChannelStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]ChannelStatus, 0, len(m.statuses))
	for _, status := range m.statuses {
		result = append(result, status)
	}
	return result
}

func (m *Manager) provider(id string) (ChannelProvider, error) {
	if m == nil || m.store == nil {
		return nil, errors.New("channel manager is unavailable")
	}
	_, found, err := m.store.GetInstance(id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("channel instance %q not found", id)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	provider := m.providers[id]
	if provider == nil {
		return nil, errors.New("channel is not running")
	}
	return provider, nil
}
func (m *Manager) SendMessage(ctx context.Context, id, chatID, content string) (string, error) {
	provider, err := m.provider(id)
	if err != nil {
		return "", err
	}
	return provider.SendMessage(ctx, chatID, content)
}

// restoreMessageContextFromHistory 只在 Hook 旁路发送前按需读取历史，避免每次
// 频道启动都扫描完整消息表。微信的主动推送没有新的入站消息可刷新 token，
// 因此必须从最近历史恢复，否则应用重启后同一个聊天虽然仍存在，发送仍会被
// provider 以“缺少回复上下文”拒绝。
func (m *Manager) restoreMessageContextFromHistory(id, chatID string) error {
	provider, err := m.provider(id)
	if err != nil {
		return err
	}
	restorer, ok := provider.(ChannelProviderContextRestorer)
	if !ok {
		return nil
	}
	session, found, err := m.store.GetSession(id, strings.TrimSpace(chatID))
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	// ListMessages 返回按时间升序的结果；按旧到新恢复，保证同一聊天最新
	// 入站消息携带的 context_token 覆盖旧 token。
	messages, err := m.store.ListMessages(session.ID, 200)
	if err != nil {
		return err
	}
	for _, message := range messages {
		restorer.RestoreMessageContext(message)
	}
	return nil
}

func (m *Manager) ReplyMessage(ctx context.Context, id, messageID, content string) (string, error) {
	provider, err := m.provider(id)
	if err != nil {
		return "", err
	}
	return provider.ReplyMessage(ctx, messageID, content)
}
func (m *Manager) ListGroups(ctx context.Context, id string) ([]ChannelGroup, error) {
	provider, err := m.provider(id)
	if err != nil {
		return nil, err
	}
	return provider.ListGroups(ctx)
}
func (m *Manager) GetGroupMessages(ctx context.Context, id, chatID string, count int) ([]ChannelMessage, error) {
	provider, err := m.provider(id)
	if err != nil {
		return nil, err
	}
	return provider.GetGroupMessages(ctx, chatID, count)
}
func (m *Manager) SendMedia(ctx context.Context, id, chatID string, media ChannelMedia, caption string) (string, error) {
	provider, err := m.provider(id)
	if err != nil {
		return "", err
	}
	return provider.SendMedia(ctx, chatID, media, caption)
}
func (m *Manager) SendStreamingMessage(ctx context.Context, id, chatID, initial, replyTo string) (StreamingHandle, error) {
	provider, err := m.provider(id)
	if err != nil {
		return nil, err
	}
	return provider.SendStreamingMessage(ctx, chatID, initial, replyTo)
}

func (m *Manager) feishuCapabilities(id string) (FeishuProviderCapabilities, error) {
	provider, err := m.provider(id)
	if err != nil {
		return nil, err
	}
	capability, ok := provider.(FeishuProviderCapabilities)
	if !ok {
		return nil, errors.New("channel provider does not support Feishu capabilities")
	}
	return capability, nil
}

func (m *Manager) weixinCapabilities(id string) (WeixinProviderCapabilities, error) {
	provider, err := m.provider(id)
	if err != nil {
		return nil, err
	}
	capability, ok := provider.(WeixinProviderCapabilities)
	if !ok {
		return nil, errors.New("channel provider does not support Weixin capabilities")
	}
	return capability, nil
}

func (m *Manager) SendFeishuImage(ctx context.Context, id, chatID string, media ChannelMedia) (string, error) {
	capability, err := m.feishuCapabilities(id)
	if err != nil {
		return "", err
	}
	return capability.SendFeishuImage(ctx, chatID, media)
}

func (m *Manager) SendFeishuFile(ctx context.Context, id, chatID string, media ChannelMedia, fileType string) (string, error) {
	capability, err := m.feishuCapabilities(id)
	if err != nil {
		return "", err
	}
	return capability.SendFeishuFile(ctx, chatID, media, fileType)
}

func (m *Manager) ListFeishuChatMembers(ctx context.Context, id, chatID string, pageSize int, pageToken, memberIDType string) (FeishuChatMemberPage, error) {
	capability, err := m.feishuCapabilities(id)
	if err != nil {
		return FeishuChatMemberPage{}, err
	}
	return capability.ListFeishuChatMembers(ctx, chatID, pageSize, pageToken, memberIDType)
}

func (m *Manager) AtFeishuMember(ctx context.Context, id, chatID string, userIDs []string, atAll bool, text string) (string, error) {
	capability, err := m.feishuCapabilities(id)
	if err != nil {
		return "", err
	}
	return capability.AtFeishuMember(ctx, chatID, userIDs, atAll, text)
}

func (m *Manager) SendFeishuUrgent(ctx context.Context, id, messageID string, userIDs, urgentTypes []string) (bool, error) {
	capability, err := m.feishuCapabilities(id)
	if err != nil {
		return false, err
	}
	return capability.SendFeishuUrgent(ctx, messageID, userIDs, urgentTypes)
}

func (m *Manager) ListFeishuBitableApps(ctx context.Context, id string, pageSize int, pageToken string) (FeishuBitableData, error) {
	capability, err := m.feishuCapabilities(id)
	if err != nil {
		return nil, err
	}
	return capability.ListFeishuBitableApps(ctx, pageSize, pageToken)
}

func (m *Manager) ListFeishuBitableTables(ctx context.Context, id, appToken string, pageSize int, pageToken string) (FeishuBitableData, error) {
	capability, err := m.feishuCapabilities(id)
	if err != nil {
		return nil, err
	}
	return capability.ListFeishuBitableTables(ctx, appToken, pageSize, pageToken)
}

func (m *Manager) ListFeishuBitableFields(ctx context.Context, id, appToken, tableID string, pageSize int, pageToken string) (FeishuBitableData, error) {
	capability, err := m.feishuCapabilities(id)
	if err != nil {
		return nil, err
	}
	return capability.ListFeishuBitableFields(ctx, appToken, tableID, pageSize, pageToken)
}

func (m *Manager) GetFeishuBitableRecords(ctx context.Context, id, appToken, tableID string, pageSize int, pageToken, filter string) (FeishuBitableData, error) {
	capability, err := m.feishuCapabilities(id)
	if err != nil {
		return nil, err
	}
	return capability.GetFeishuBitableRecords(ctx, appToken, tableID, pageSize, pageToken, filter)
}

func (m *Manager) CreateFeishuBitableRecords(ctx context.Context, id, appToken, tableID string, records []map[string]any) (FeishuBitableData, error) {
	capability, err := m.feishuCapabilities(id)
	if err != nil {
		return nil, err
	}
	return capability.CreateFeishuBitableRecords(ctx, appToken, tableID, records)
}

func (m *Manager) UpdateFeishuBitableRecords(ctx context.Context, id, appToken, tableID string, records []map[string]any) (FeishuBitableData, error) {
	capability, err := m.feishuCapabilities(id)
	if err != nil {
		return nil, err
	}
	return capability.UpdateFeishuBitableRecords(ctx, appToken, tableID, records)
}

func (m *Manager) DeleteFeishuBitableRecords(ctx context.Context, id, appToken, tableID string, recordIDs []string) (FeishuBitableData, error) {
	capability, err := m.feishuCapabilities(id)
	if err != nil {
		return nil, err
	}
	return capability.DeleteFeishuBitableRecords(ctx, appToken, tableID, recordIDs)
}

func (m *Manager) SendWeixinImage(ctx context.Context, id, chatID string, media ChannelMedia, caption string) (string, error) {
	capability, err := m.weixinCapabilities(id)
	if err != nil {
		return "", err
	}
	return capability.SendWeixinImage(ctx, chatID, media, caption)
}

func (m *Manager) SendWeixinFile(ctx context.Context, id, chatID string, media ChannelMedia, caption string) (string, error) {
	capability, err := m.weixinCapabilities(id)
	if err != nil {
		return "", err
	}
	return capability.SendWeixinFile(ctx, chatID, media, caption)
}
