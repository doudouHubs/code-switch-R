package channels

import "context"

type EventSink func(ChannelEvent)

type ChannelProvider interface {
	Start(context.Context) error
	Stop(context.Context) error
	IsRunning() bool
	SendMessage(context.Context, string, string) (string, error)
	ReplyMessage(context.Context, string, string) (string, error)
	GetGroupMessages(context.Context, string, int) ([]ChannelMessage, error)
	ListGroups(context.Context) ([]ChannelGroup, error)
	SendMedia(context.Context, string, ChannelMedia, string) (string, error)
	SupportsStreaming() bool
	SendStreamingMessage(context.Context, string, string, string) (StreamingHandle, error)
}

// ChannelProviderContextRestorer 是可从持久化消息历史恢复发送上下文的可选能力。
// 只有平台 provider 自己知道 raw 载荷里的上下文字段含义，Manager 只负责把
// 已保存的消息交给它解析，避免把微信 token 之类的平台协议泄漏到通用层。
type ChannelProviderContextRestorer interface {
	RestoreMessageContext(ChannelMessage)
}

type ChannelGroup struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MemberCount int    `json:"memberCount,omitempty"`
	Raw         string `json:"raw,omitempty"`
}

type StreamingHandle interface {
	Update(context.Context, string) error
	Finish(context.Context, string) error
}

type ProviderFactory func(ChannelInstance, EventSink) (ChannelProvider, error)
