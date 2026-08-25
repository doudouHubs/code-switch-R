package channels

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

type relayProvider struct {
	instance     ChannelInstance
	notify       EventSink
	wsURL        string
	ws           *wsClient
	running      bool
	mu           sync.RWMutex
	messageChats map[string]string
}

func newRelayProvider(instance ChannelInstance, notify EventSink) *relayProvider {
	return &relayProvider{instance: instance, notify: notify, wsURL: strings.TrimSpace(instance.Config["wsUrl"]), messageChats: make(map[string]string)}
}

func (p *relayProvider) startRelay(ctx context.Context) error {
	p.mu.Lock()
	p.running = true
	p.mu.Unlock()
	if p.wsURL == "" {
		// 与 OpenCowork 的 BasePluginService 保持一致：没有配置 relay 时，服务本身
		// 仍可运行，发送能力由具体平台 API 决定，入站能力等待用户补齐 wsUrl。
		return nil
	}
	client, err := newWSClient(ctx, p.wsURL, func(raw []byte) { p.handleRaw(raw) }, func(err error) {
		p.emit("error", err.Error())
	})
	if err != nil {
		p.mu.Lock()
		p.running = false
		p.mu.Unlock()
		return err
	}
	p.mu.Lock()
	p.ws = client
	p.mu.Unlock()
	return nil
}

func (p *relayProvider) stopRelay(ctx context.Context) error {
	_ = ctx
	p.mu.Lock()
	client := p.ws
	p.ws = nil
	p.running = false
	p.mu.Unlock()
	if client != nil {
		return client.Close()
	}
	return nil
}

func (p *relayProvider) isRunning() bool { p.mu.RLock(); defer p.mu.RUnlock(); return p.running }

func (p *relayProvider) emit(eventType string, data any) {
	if p.notify == nil {
		return
	}
	p.notify(ChannelEvent{Type: eventType, InstanceID: p.instance.ID, PluginType: p.instance.Type, Data: data, At: nowMillis()})
}

func (p *relayProvider) handleRaw(raw []byte) {
	message, ok := parseIncoming(p.instance.Type, raw)
	if !ok {
		return
	}
	message.InstanceID = p.instance.ID
	p.mu.Lock()
	p.messageChats[message.ExternalID] = message.ChatID
	if len(p.messageChats) > 1000 {
		for id := range p.messageChats {
			delete(p.messageChats, id)
			break
		}
	}
	p.mu.Unlock()
	p.emit("incoming_message", message)
}

func (p *relayProvider) writeRelay(ctx context.Context, payload any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	p.mu.RLock()
	client := p.ws
	running := p.running
	p.mu.RUnlock()
	if !running || client == nil {
		return errors.New("channel relay is not connected")
	}
	return client.WriteJSON(payload)
}

func (p *relayProvider) relaySend(ctx context.Context, action, chatID, content string, media ChannelMedia) (string, error) {
	messageID := fmt.Sprintf("%s-%d", p.instance.ID, nowMillis())
	if err := p.writeRelay(ctx, relayPayload(action, chatID, content, media)); err != nil {
		return "", err
	}
	return messageID, nil
}

func (p *relayProvider) relayReplyChat(messageID string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.messageChats[messageID]
}

func (p *relayProvider) relayGroups(ctx context.Context) ([]ChannelGroup, error) {
	if err := p.writeRelay(ctx, "list_groups"); err != nil {
		return nil, err
	}
	return []ChannelGroup{}, nil
}

func (p *relayProvider) relayMessages(ctx context.Context, chatID string, count int) ([]ChannelMessage, error) {
	if count <= 0 {
		count = 20
	}
	if err := p.writeRelay(ctx, map[string]any{"action": "get_messages", "chatId": chatID, "count": count}); err != nil {
		return nil, err
	}
	return []ChannelMessage{}, nil
}

func (p *relayProvider) relayMedia(ctx context.Context, chatID string, media ChannelMedia, caption string) (string, error) {
	return p.relaySend(ctx, "send_media", chatID, caption, media)
}

func (p *relayProvider) relayStreaming(ctx context.Context, chatID, initial, replyTo string) (StreamingHandle, error) {
	messageID, err := p.relaySend(ctx, "stream_start", chatID, initial, ChannelMedia{})
	if err != nil {
		return nil, err
	}
	return &relayStreamingHandle{provider: p, chatID: chatID, messageID: messageID, replyTo: replyTo}, nil
}

type relayStreamingHandle struct {
	provider                   *relayProvider
	chatID, messageID, replyTo string
	mu                         sync.Mutex
	finished                   bool
}

func (h *relayStreamingHandle) MessageID() string {
	if h == nil {
		return ""
	}
	return h.messageID
}

func (h *relayStreamingHandle) Update(ctx context.Context, content string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.finished {
		return nil
	}
	return h.provider.writeRelay(ctx, map[string]any{"action": "stream_update", "chatId": h.chatID, "messageId": h.messageID, "replyToMessageId": h.replyTo, "content": content})
}
func (h *relayStreamingHandle) Finish(ctx context.Context, content string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.finished {
		return nil
	}
	h.finished = true
	return h.provider.writeRelay(ctx, map[string]any{"action": "stream_finish", "chatId": h.chatID, "messageId": h.messageID, "replyToMessageId": h.replyTo, "content": content})
}
