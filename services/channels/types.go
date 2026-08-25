package channels

import (
	"context"
	"encoding/json"
	"time"
)

const (
	ChannelTypeFeishu   = "feishu-bot"
	ChannelTypeDingTalk = "dingtalk-bot"
	ChannelTypeWeCom    = "wecom-bot"
	ChannelTypeQQ       = "qq-bot"
	ChannelTypeWeixin   = "weixin-official"
	ChannelTypeTelegram = "telegram-bot"
	ChannelTypeDiscord  = "discord-bot"
	ChannelTypeWhatsApp = "whatsapp-bot"
)

// BuiltinChannelTypes 是实例补齐和 provider 注册共用的唯一平台顺序。
// 顺序稳定后，首次导入、前端展示和测试快照不会因为 map 遍历顺序产生漂移。
var BuiltinChannelTypes = []string{
	ChannelTypeFeishu,
	ChannelTypeDingTalk,
	ChannelTypeWeCom,
	ChannelTypeQQ,
	ChannelTypeWeixin,
	ChannelTypeTelegram,
	ChannelTypeDiscord,
	ChannelTypeWhatsApp,
}

type ConfigField struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Secret      bool   `json:"secret,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
}

type ProviderDescriptor struct {
	Type         string        `json:"type"`
	DisplayName  string        `json:"displayName"`
	Description  string        `json:"description"`
	Icon         string        `json:"icon"`
	Builtin      bool          `json:"builtin"`
	Tools        []string      `json:"tools"`
	ConfigSchema []ConfigField `json:"configSchema"`
}

const (
	ChannelToolFeishuSendImage          = "FeishuSendImage"
	ChannelToolFeishuSendFile           = "FeishuSendFile"
	ChannelToolFeishuListChatMembers    = "FeishuListChatMembers"
	ChannelToolFeishuAtMember           = "FeishuAtMember"
	ChannelToolFeishuSendUrgent         = "FeishuSendUrgent"
	ChannelToolFeishuBitableListApps    = "FeishuBitableListApps"
	ChannelToolFeishuBitableListTables  = "FeishuBitableListTables"
	ChannelToolFeishuBitableListFields  = "FeishuBitableListFields"
	ChannelToolFeishuBitableGetRecords  = "FeishuBitableGetRecords"
	ChannelToolFeishuBitableCreate      = "FeishuBitableCreateRecords"
	ChannelToolFeishuBitableUpdate      = "FeishuBitableUpdateRecords"
	ChannelToolFeishuBitableDelete      = "FeishuBitableDeleteRecords"
	ChannelToolWeixinSendImage          = "WeixinSendImage"
	ChannelToolWeixinSendFile           = "WeixinSendFile"
)

// FeishuChatMemberPage 保留飞书分页响应中的成员字段，同时把未知字段放入 Raw，
// 这样版本新增字段时不会破坏 Agent 已经依赖的 page_token/has_more 结构。
type FeishuChatMemberPage struct {
	Items     []map[string]any `json:"items"`
	PageToken string           `json:"page_token,omitempty"`
	HasMore   bool             `json:"has_more,omitempty"`
}

// FeishuBitableData 使用原始 JSON 承载飞书 Bitable 的动态字段结构；请求参数仍然
// 使用明确的 app/table/record 类型，避免把平台路由退化成任意 RPC。
type FeishuBitableData json.RawMessage

// FeishuProviderCapabilities 是 Feishu 专用 Agent 工具的唯一 provider 边界。
// 平台特有 API 不能塞进通用 ChannelProvider，否则其它平台会被迫实现无意义方法。
type FeishuProviderCapabilities interface {
	SendFeishuImage(context.Context, string, ChannelMedia) (string, error)
	SendFeishuFile(context.Context, string, ChannelMedia, string) (string, error)
	ListFeishuChatMembers(context.Context, string, int, string, string) (FeishuChatMemberPage, error)
	AtFeishuMember(context.Context, string, []string, bool, string) (string, error)
	SendFeishuUrgent(context.Context, string, []string, []string) (bool, error)
	ListFeishuBitableApps(context.Context, int, string) (FeishuBitableData, error)
	ListFeishuBitableTables(context.Context, string, int, string) (FeishuBitableData, error)
	ListFeishuBitableFields(context.Context, string, string, int, string) (FeishuBitableData, error)
	GetFeishuBitableRecords(context.Context, string, string, int, string, string) (FeishuBitableData, error)
	CreateFeishuBitableRecords(context.Context, string, string, []map[string]any) (FeishuBitableData, error)
	UpdateFeishuBitableRecords(context.Context, string, string, []map[string]any) (FeishuBitableData, error)
	DeleteFeishuBitableRecords(context.Context, string, string, []string) (FeishuBitableData, error)
}

// WeixinProviderCapabilities 只暴露原版官方微信媒体发送能力；凭据和上下文 token
// 仍由 weixinProvider 内部维护，调用方不能伪造 token 直接发请求。
type WeixinProviderCapabilities interface {
	SendWeixinImage(context.Context, string, ChannelMedia, string) (string, error)
	SendWeixinFile(context.Context, string, ChannelMedia, string) (string, error)
}

type ChannelFeatures struct {
	AutoReply      bool `json:"autoReply"`
	StreamingReply bool `json:"streamingReply"`
	AutoStart      bool `json:"autoStart"`
}

type ChannelPermissions struct {
	AllowReadHome        bool     `json:"allowReadHome"`
	ReadablePathPrefixes []string `json:"readablePathPrefixes"`
	AllowWriteOutside    bool     `json:"allowWriteOutside"`
	AllowShell           bool     `json:"allowShell"`
	AllowSubAgents       bool     `json:"allowSubAgents"`
}

type ChannelInstance struct {
	ID               string             `json:"id"`
	Type             string             `json:"type"`
	Name             string             `json:"name"`
	Enabled          bool               `json:"enabled"`
	Builtin          bool               `json:"builtin"`
	Archived         bool               `json:"archived,omitempty"`
	Config           map[string]string  `json:"config"`
	CreatedAt        int64              `json:"createdAt"`
	ProjectID        *string            `json:"projectId,omitempty"`
	ProviderPlatform string             `json:"providerPlatform,omitempty"`
	ProviderID       *string            `json:"providerId,omitempty"`
	Model            *string            `json:"model,omitempty"`
	Tools            map[string]bool    `json:"tools,omitempty"`
	Features         ChannelFeatures    `json:"features"`
	Permissions      ChannelPermissions `json:"permissions"`
	Status           string             `json:"status"`
	LastError        string             `json:"lastError,omitempty"`
	UpdatedAt        int64              `json:"updatedAt"`
}

// BuiltinProviderDescriptors 返回频道配置页和 provider 注册共用的静态能力描述。
// descriptor 只描述配置字段和允许暴露给 Agent 的工具名，不携带任何凭据或运行时状态。
func BuiltinProviderDescriptors() []ProviderDescriptor {
	commonTools := []string{
		"PluginSendMessage", "PluginReplyMessage", "PluginGetGroupMessages",
		"PluginListGroups", "PluginSummarizeGroup", "PluginGetCurrentChatMessages",
	}
	feishuTools := append(append([]string{}, commonTools...),
		ChannelToolFeishuSendImage, ChannelToolFeishuSendFile, ChannelToolFeishuListChatMembers,
		ChannelToolFeishuAtMember, ChannelToolFeishuSendUrgent, ChannelToolFeishuBitableListApps,
		ChannelToolFeishuBitableListTables, ChannelToolFeishuBitableListFields,
		ChannelToolFeishuBitableGetRecords, ChannelToolFeishuBitableCreate,
		ChannelToolFeishuBitableUpdate, ChannelToolFeishuBitableDelete,
	)
	weixinTools := append(append([]string{}, commonTools...), ChannelToolWeixinSendImage, ChannelToolWeixinSendFile)
	relay := ConfigField{Key: "wsUrl", Label: "Relay WebSocket URL", Placeholder: "wss://your-relay-server/ws"}
	return []ProviderDescriptor{
		{Type: ChannelTypeFeishu, DisplayName: "Feishu Bot", Description: "Lark/Feishu messaging bot", Icon: "feishu", Builtin: true, Tools: feishuTools, ConfigSchema: []ConfigField{{Key: "appId", Label: "App ID", Required: true, Placeholder: "cli_xxxxx"}, {Key: "appSecret", Label: "App Secret", Secret: true, Required: true}}},
		{Type: ChannelTypeDingTalk, DisplayName: "DingTalk Bot", Description: "DingTalk messaging bot", Icon: "dingtalk", Builtin: true, Tools: commonTools, ConfigSchema: []ConfigField{{Key: "appKey", Label: "App Key", Required: true}, {Key: "appSecret", Label: "App Secret", Secret: true, Required: true}, {Key: "cardTemplateId", Label: "Streaming card template ID", Placeholder: "Optional"}}},
		{Type: ChannelTypeWeCom, DisplayName: "WeCom Bot", Description: "WeCom messaging bot", Icon: "wecom", Builtin: true, Tools: commonTools, ConfigSchema: []ConfigField{{Key: "corpId", Label: "Corp ID", Required: true}, {Key: "secret", Label: "Secret", Secret: true, Required: true}, {Key: "agentId", Label: "Agent ID", Required: true}, relay}},
		{Type: ChannelTypeQQ, DisplayName: "QQ Bot", Description: "Tencent QQ Bot", Icon: "qq", Builtin: true, Tools: commonTools, ConfigSchema: []ConfigField{{Key: "appId", Label: "App ID", Required: true}, {Key: "clientSecret", Label: "Client Secret", Secret: true, Required: true}, {Key: "useSandbox", Label: "Sandbox", Placeholder: "true / false"}, {Key: "markdownSupport", Label: "Markdown support", Placeholder: "true / false"}}},
		{Type: ChannelTypeWeixin, DisplayName: "WeChat Official", Description: "WeChat Official channel (QR login + long polling)", Icon: "wechat", Builtin: true, Tools: weixinTools, ConfigSchema: []ConfigField{{Key: "baseUrl", Label: "Base URL", Placeholder: "https://ilinkai.weixin.qq.com"}, {Key: "routeTag", Label: "Route tag"}, {Key: "cdnBaseUrl", Label: "CDN Base URL", Placeholder: "https://novac2c.cdn.weixin.qq.com/c2c"}, {Key: "token", Label: "Token", Secret: true}, {Key: "accountId", Label: "Account ID"}, {Key: "userId", Label: "User ID"}}},
		{Type: ChannelTypeTelegram, DisplayName: "Telegram Bot", Description: "Telegram messaging bot", Icon: "telegram", Builtin: true, Tools: commonTools, ConfigSchema: []ConfigField{{Key: "botToken", Label: "Bot Token", Secret: true, Required: true}, relay}},
		{Type: ChannelTypeDiscord, DisplayName: "Discord Bot", Description: "Discord messaging bot", Icon: "discord", Builtin: true, Tools: commonTools, ConfigSchema: []ConfigField{{Key: "botToken", Label: "Bot Token", Secret: true, Required: true}}},
		{Type: ChannelTypeWhatsApp, DisplayName: "WhatsApp Bot", Description: "WhatsApp Cloud API bot", Icon: "whatsapp", Builtin: true, Tools: commonTools, ConfigSchema: []ConfigField{{Key: "phoneNumberId", Label: "Phone number ID", Required: true}, {Key: "accessToken", Label: "Access token", Secret: true, Required: true}, relay}},
	}
}

type ChannelSession struct {
	ID            string `json:"id"`
	InstanceID    string `json:"instanceId"`
	ChatID        string `json:"chatId"`
	ChatName      string `json:"chatName,omitempty"`
	SenderID      string `json:"senderId,omitempty"`
	SenderName    string `json:"senderName,omitempty"`
	ProjectID     string `json:"projectId"`
	WorkingFolder string `json:"workingFolder"`
	CreatedAt     int64  `json:"createdAt"`
	UpdatedAt     int64  `json:"updatedAt"`
}

type ChannelMessage struct {
	ID         string         `json:"id"`
	InstanceID string         `json:"instanceId"`
	SessionID  string         `json:"sessionId,omitempty"`
	ExternalID string         `json:"externalId,omitempty"`
	Role       string         `json:"role"`
	ChatID     string         `json:"chatId"`
	SenderID   string         `json:"senderId,omitempty"`
	SenderName string         `json:"senderName,omitempty"`
	Content    string         `json:"content"`
	Images     []ChannelMedia `json:"images,omitempty"`
	Audio      *ChannelMedia  `json:"audio,omitempty"`
	Timestamp  int64          `json:"timestamp"`
	Raw        string         `json:"raw,omitempty"`
}

type ChannelMedia struct {
	ID        string `json:"id,omitempty"`
	Kind      string `json:"kind"`
	MediaType string `json:"mediaType"`
	FileName  string `json:"fileName,omitempty"`
	Data      []byte `json:"data,omitempty"`
}

type ChannelEvent struct {
	Type       string `json:"type"`
	InstanceID string `json:"instanceId"`
	PluginType string `json:"pluginType"`
	Data       any    `json:"data,omitempty"`
	At         int64  `json:"at"`
}

type ChannelStatus struct {
	InstanceID string `json:"instanceId"`
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
	UpdatedAt  int64  `json:"updatedAt"`
}

type ProjectBinding struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Name string `json:"name"`
}

type ImportReport struct {
	AlreadyApplied int `json:"alreadyApplied"`
	Imported       int `json:"imported"`
	Templates      int `json:"templates"`
	Skipped        int `json:"skipped"`
}

func nowMillis() int64 { return time.Now().UnixMilli() }

func defaultFeatures() ChannelFeatures {
	return ChannelFeatures{AutoReply: true, StreamingReply: true, AutoStart: true}
}

func defaultPermissions() ChannelPermissions {
	return ChannelPermissions{
		AllowReadHome:        false,
		ReadablePathPrefixes: []string{},
		AllowWriteOutside:    false,
		AllowShell:           false,
		AllowSubAgents:       false,
	}
}

func normalizeInstance(instance ChannelInstance) ChannelInstance {
	if instance.Config == nil {
		instance.Config = map[string]string{}
	}
	if instance.Features == (ChannelFeatures{}) {
		instance.Features = defaultFeatures()
	}
	if instance.Permissions.ReadablePathPrefixes == nil {
		instance.Permissions = defaultPermissions()
	}
	if instance.Tools == nil {
		instance.Tools = map[string]bool{}
	}
	if instance.Status == "" {
		instance.Status = "stopped"
	}
	if instance.CreatedAt == 0 {
		instance.CreatedAt = nowMillis()
	}
	if instance.UpdatedAt == 0 {
		instance.UpdatedAt = instance.CreatedAt
	}
	return instance
}
