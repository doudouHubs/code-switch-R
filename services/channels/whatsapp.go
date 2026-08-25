package channels

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type whatsappProvider struct {
	*relayProvider
	phoneID, token string
	client         *http.Client
}

func newWhatsAppProvider(instance ChannelInstance, notify EventSink) (ChannelProvider, error) {
	phone := strings.TrimSpace(instance.Config["phoneNumberId"])
	token := strings.TrimSpace(instance.Config["accessToken"])
	if phone == "" || token == "" {
		return nil, errors.New("WhatsApp phoneNumberId and accessToken are required")
	}
	return &whatsappProvider{relayProvider: newRelayProvider(instance, notify), phoneID: phone, token: token, client: channelHTTPClient()}, nil
}
func (p *whatsappProvider) endpoint() string {
	return "https://graph.facebook.com/v18.0/" + p.phoneID + "/messages"
}
func (p *whatsappProvider) headers() map[string]string {
	return stringMap("Authorization", "Bearer "+p.token)
}
func (p *whatsappProvider) Start(ctx context.Context) error {
	var value map[string]any
	if err := doJSON(ctx, p.client, http.MethodGet, "https://graph.facebook.com/v18.0/"+p.phoneID, p.headers(), nil, &value); err != nil {
		return err
	}
	if err := parseJSONError(value); err != nil {
		return err
	}
	return p.startRelay(ctx)
}
func (p *whatsappProvider) Stop(ctx context.Context) error { return p.stopRelay(ctx) }
func (p *whatsappProvider) IsRunning() bool                { return p.isRunning() }
func (p *whatsappProvider) SendMessage(ctx context.Context, chatID, content string) (string, error) {
	if p.wsURL != "" {
		return p.relaySend(ctx, "send_message", chatID, content, ChannelMedia{})
	}
	var value struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	body := map[string]any{"messaging_product": "whatsapp", "to": chatID, "type": "text", "text": map[string]string{"body": content}}
	if err := doJSON(ctx, p.client, http.MethodPost, p.endpoint(), p.headers(), body, &value); err != nil {
		return "", err
	}
	if len(value.Messages) == 0 {
		return "", errors.New("WhatsApp response has no message id")
	}
	return value.Messages[0].ID, nil
}
func (p *whatsappProvider) ReplyMessage(ctx context.Context, messageID, content string) (string, error) {
	chatID := p.relayReplyChat(messageID)
	if chatID == "" {
		return "", errors.New("WhatsApp reply context not found")
	}
	return p.SendMessage(ctx, chatID, content)
}
func (p *whatsappProvider) GetGroupMessages(context.Context, string, int) ([]ChannelMessage, error) {
	return []ChannelMessage{}, nil
}
func (p *whatsappProvider) ListGroups(context.Context) ([]ChannelGroup, error) {
	return []ChannelGroup{}, nil
}
func (p *whatsappProvider) SendMedia(ctx context.Context, chatID string, media ChannelMedia, caption string) (string, error) {
	if p.wsURL != "" {
		return p.relayMedia(ctx, chatID, media, caption)
	}
	return "", fmt.Errorf("WhatsApp media delivery requires a public media URL or relay")
}
func (p *whatsappProvider) SupportsStreaming() bool { return false }
func (p *whatsappProvider) SendStreamingMessage(context.Context, string, string, string) (StreamingHandle, error) {
	return nil, errors.New("WhatsApp streaming is not supported")
}
