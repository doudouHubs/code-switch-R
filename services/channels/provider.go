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
