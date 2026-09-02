package channels

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// ProjectProvider 是 Wails service 获取当前项目列表的最小边界。
// 项目扫描和频道配置分属不同 owner，频道只保存项目 ID，不复制项目目录元数据。
type ProjectProvider func() ([]ProjectBinding, error)

// ChannelService 是 Wails 与频道 runtime 的薄适配层。
// 业务校验仍由 Store/Manager/AgentRuntime 负责，API 只做参数归一化和生命周期编排。
type ChannelService struct {
	store           *Store
	manager         *Manager
	projectProvider ProjectProvider
	eventSink       EventSink
	weixinLoginMu   sync.Mutex
	weixinLogins    map[string]*weixinLoginSession
}

func NewChannelService(store *Store, manager *Manager, projectProvider ProjectProvider, eventSinks ...EventSink) *ChannelService {
	var eventSink EventSink
	if len(eventSinks) > 0 {
		eventSink = eventSinks[0]
	}
	return &ChannelService{store: store, manager: manager, projectProvider: projectProvider, eventSink: eventSink, weixinLogins: make(map[string]*weixinLoginSession)}
}

func (s *ChannelService) ListDescriptors() []ProviderDescriptor {
	return BuiltinProviderDescriptors()
}

func (s *ChannelService) ListInstances() ([]ChannelInstance, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("channel service is unavailable")
	}
	return s.store.ListInstances()
}

func (s *ChannelService) GetInstance(id string) (ChannelInstance, bool, error) {
	if s == nil || s.store == nil {
		return ChannelInstance{}, false, errors.New("channel service is unavailable")
	}
	return s.store.GetInstance(strings.TrimSpace(id))
}

func (s *ChannelService) ListProjects() ([]ProjectBinding, error) {
	if s == nil || s.projectProvider == nil {
		return []ProjectBinding{}, nil
	}
	return s.projectProvider()
}

// SaveInstance 保存配置；如果实例原本在运行，会先停旧 provider，再按新配置重新启动。
// 这是必要的生命周期边界：token、WebSocket 和 webhook 上下文不能继续使用旧配置。
func (s *ChannelService) SaveInstance(instance ChannelInstance) error {
	if s == nil || s.store == nil || s.manager == nil {
		return errors.New("channel service is unavailable")
	}
	instance.ID = strings.TrimSpace(instance.ID)
	instance.Type = strings.TrimSpace(instance.Type)
	if instance.ID == "" || instance.Type == "" {
		return errors.New("channel instance id and type are required")
	}
	previous, found, err := s.store.GetInstance(instance.ID)
	if err != nil {
		return err
	}
	wasRunning := found && s.manager.Status(instance.ID).State == "running"
	if wasRunning {
		if err := s.manager.Stop(context.Background(), instance.ID); err != nil {
			return err
		}
	}
	if found && instance.CreatedAt == 0 {
		instance.CreatedAt = previous.CreatedAt
	}
	if found && instance.Builtin == false {
		instance.Builtin = previous.Builtin
	}
	if err := s.store.UpsertInstance(instance); err != nil {
		return err
	}
	if instance.Enabled && wasRunning {
		return s.manager.Start(context.Background(), instance.ID)
	}
	return nil
}

// RemoveInstance 只删除用户创建的实例；内置实例是平台 canonical 配置入口。
func (s *ChannelService) RemoveInstance(id string) error {
	if s == nil || s.store == nil || s.manager == nil {
		return errors.New("channel service is unavailable")
	}
	id = strings.TrimSpace(id)
	instance, found, err := s.store.GetInstance(id)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("channel instance not found")
	}
	if instance.Builtin {
		return errors.New("builtin channel cannot be removed")
	}
	// 先停 provider 再删记录，确保 WebSocket、轮询和事件回调不会继续引用已删除实例。
	if err := s.manager.Stop(context.Background(), id); err != nil {
		return err
	}
	return s.store.DeleteInstance(id)
}

func (s *ChannelService) BindProject(id string, projectID *string) error {
	if s == nil || s.store == nil {
		return errors.New("channel service is unavailable")
	}
	instance, found, err := s.store.GetInstance(strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if !found {
		return errors.New("channel instance not found")
	}
	if projectID != nil {
		value := strings.TrimSpace(*projectID)
		if value == "" {
			projectID = nil
		} else {
			projectID = &value
		}
	}
	if instance.ProjectID != nil && projectID != nil && strings.TrimSpace(*instance.ProjectID) != strings.TrimSpace(*projectID) {
		_ = s.manager.Stop(context.Background(), instance.ID)
	}
	if projectID == nil {
		instance.Enabled = false
		instance.Status = "stopped"
	}
	instance.ProjectID = projectID
	instance.UpdatedAt = nowMillis()
	return s.store.UpsertInstance(instance)
}

func (s *ChannelService) SetEnabled(id string, enabled bool) error {
	if s == nil || s.store == nil || s.manager == nil {
		return errors.New("channel service is unavailable")
	}
	instance, found, err := s.store.GetInstance(strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if !found {
		return errors.New("channel instance not found")
	}
	if !enabled {
		if err := s.manager.Stop(context.Background(), instance.ID); err != nil {
			return err
		}
		instance.Enabled = false
		instance.Status = "stopped"
		return s.store.UpsertInstance(instance)
	}
	if instance.ProjectID == nil || strings.TrimSpace(*instance.ProjectID) == "" {
		return errors.New("channel must be bound to a project before it can be enabled")
	}
	instance.Enabled = true
	instance.UpdatedAt = nowMillis()
	return s.store.UpsertInstance(instance)
}

func (s *ChannelService) Start(id string) error {
	if s == nil || s.manager == nil {
		return errors.New("channel service is unavailable")
	}
	return s.manager.Start(context.Background(), strings.TrimSpace(id))
}

func (s *ChannelService) Stop(id string) error {
	if s == nil || s.manager == nil {
		return errors.New("channel service is unavailable")
	}
	return s.manager.Stop(context.Background(), strings.TrimSpace(id))
}

func (s *ChannelService) GetStatus(id string) ChannelStatus {
	if s == nil || s.manager == nil {
		return ChannelStatus{InstanceID: strings.TrimSpace(id), State: "stopped"}
	}
	return s.manager.Status(strings.TrimSpace(id))
}

func (s *ChannelService) ListStatuses() []ChannelStatus {
	if s == nil || s.manager == nil {
		return []ChannelStatus{}
	}
	return s.manager.Statuses()
}

func (s *ChannelService) ListSessions(instanceID string) ([]ChannelSession, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("channel service is unavailable")
	}
	return s.store.ListSessions(strings.TrimSpace(instanceID))
}

func (s *ChannelService) ListMessages(sessionID string, limit int) ([]ChannelMessage, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("channel service is unavailable")
	}
	return s.store.ListMessages(strings.TrimSpace(sessionID), limit)
}

func (s *ChannelService) ListGroups(instanceID string) ([]ChannelGroup, error) {
	if s == nil || s.manager == nil {
		return nil, errors.New("channel service is unavailable")
	}
	return s.manager.ListGroups(context.Background(), strings.TrimSpace(instanceID))
}

func (s *ChannelService) GetGroupMessages(instanceID, chatID string, count int) ([]ChannelMessage, error) {
	if s == nil || s.manager == nil {
		return nil, errors.New("channel service is unavailable")
	}
	return s.manager.GetGroupMessages(context.Background(), strings.TrimSpace(instanceID), strings.TrimSpace(chatID), count)
}

func (s *ChannelService) SendMessage(instanceID, chatID, content string) (string, error) {
	if s == nil || s.manager == nil {
		return "", errors.New("channel service is unavailable")
	}
	instanceID = strings.TrimSpace(instanceID)
	chatID = strings.TrimSpace(chatID)
	content = strings.TrimSpace(content)
	messageID, err := s.manager.SendMessage(context.Background(), instanceID, chatID, content)
	if err != nil {
		return "", err
	}
	if s.store == nil {
		return messageID, nil
	}
	instance, found, getErr := s.store.GetInstance(instanceID)
	if getErr != nil {
		return messageID, getErr
	}
	if !found {
		return messageID, nil
	}
	if persistErr := appendChannelOutboundMessage(s.store, s.eventSink, instance, chatID, content, messageID); persistErr != nil {
		// 平台发送已经成功，不能把持久化错误伪装成发送失败再诱导前端重试；
		// 通过频道 error 事件暴露事实，调用方仍拿到真实平台 message ID。
		if s.eventSink != nil {
			s.eventSink(ChannelEvent{Type: "error", InstanceID: instance.ID, PluginType: instance.Type, Data: persistErr.Error(), At: nowMillis()})
		}
	}
	return messageID, nil
}

func (s *ChannelService) StartAuto() ([]string, []string, error) {
	if s == nil || s.manager == nil {
		return nil, nil, errors.New("channel service is unavailable")
	}
	return s.manager.StartAuto(context.Background())
}

func (s *ChannelService) StopAll() error {
	if s == nil || s.manager == nil {
		return nil
	}
	return s.manager.StopAll(context.Background())
}
