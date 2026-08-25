package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"
)

type discordProvider struct {
	*relayProvider
	token           string
	client          *http.Client
	heartbeatCancel context.CancelFunc
	heartbeat       sync.WaitGroup
}

func newDiscordProvider(instance ChannelInstance, notify EventSink) (ChannelProvider, error) {
	if strings.TrimSpace(instance.Config["botToken"]) == "" {
		return nil, errors.New("Discord botToken is required")
	}
	return &discordProvider{relayProvider: newRelayProvider(instance, notify), token: strings.TrimSpace(instance.Config["botToken"]), client: channelHTTPClient()}, nil
}
func (p *discordProvider) apiURL(path string) string { return "https://discord.com/api/v10" + path }
func (p *discordProvider) headers() map[string]string {
	return stringMap("Authorization", "Bot "+p.token)
}
func (p *discordProvider) Start(ctx context.Context) error {
	if err := doJSON(ctx, p.client, http.MethodGet, p.apiURL("/users/@me"), p.headers(), nil, nil); err != nil {
		return err
	}
	if p.wsURL != "" {
		return p.startRelay(ctx)
	}
	client, err := newWSClient(ctx, "wss://gateway.discord.gg/?v=10&encoding=json", func(raw []byte) { p.handleGateway(raw) }, func(err error) { p.emit("error", err.Error()) })
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.ws = client
	p.running = true
	p.mu.Unlock()
	return nil
}
func (p *discordProvider) handleGateway(raw []byte) {
	value := decodeJSONObject(raw)
	if value == nil {
		return
	}
	op := int(firstNumber(value, "op"))
	switch op {
	case 10:
		hello, _ := value["d"].(map[string]any)
		interval := int(firstNumber(hello, "heartbeat_interval"))
		if interval <= 0 {
			interval = 41250
		}
		ctx, cancel := context.WithCancel(context.Background())
		p.heartbeatCancel = cancel
		p.heartbeat.Add(1)
		go func() {
			defer p.heartbeat.Done()
			ticker := time.NewTicker(time.Duration(interval) * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					_ = p.ws.WriteJSON(map[string]any{"op": 1, "d": nil})
				case <-ctx.Done():
					return
				}
			}
		}()
		_ = p.ws.WriteJSON(map[string]any{"op": 2, "d": map[string]any{"token": p.token, "intents": 513, "properties": map[string]string{"os": "windows", "browser": "code-switch", "device": "code-switch"}}})
	case 0:
		if event, _ := value["t"].(string); event == "MESSAGE_CREATE" {
			if message, ok := parseIncoming(ChannelTypeDiscord, raw); ok {
				message.InstanceID = p.instance.ID
				p.messageChats[message.ExternalID] = message.ChatID
				p.emit("incoming_message", message)
			}
		}
	}
}
func (p *discordProvider) Stop(ctx context.Context) error {
	if p.heartbeatCancel != nil {
		p.heartbeatCancel()
		p.heartbeatCancel = nil
		p.heartbeat.Wait()
	}
	return p.stopRelay(ctx)
}
func (p *discordProvider) IsRunning() bool { return p.isRunning() }
func (p *discordProvider) SendMessage(ctx context.Context, chatID, content string) (string, error) {
	if p.wsURL != "" {
		return p.relaySend(ctx, "send_message", chatID, content, ChannelMedia{})
	}
	var value struct {
		ID   string `json:"id"`
		Code int    `json:"code"`
	}
	if err := doJSON(ctx, p.client, http.MethodPost, p.apiURL("/channels/"+chatID+"/messages"), p.headers(), map[string]string{"content": content}, &value); err != nil {
		return "", err
	}
	if value.Code != 0 {
		return "", fmt.Errorf("Discord rejected sendMessage: %d", value.Code)
	}
	return value.ID, nil
}
func (p *discordProvider) ReplyMessage(ctx context.Context, messageID, content string) (string, error) {
	chatID := p.relayReplyChat(messageID)
	if chatID == "" {
		return "", errors.New("Discord reply context not found")
	}
	var value struct {
		ID   string `json:"id"`
		Code int    `json:"code"`
	}
	if err := doJSON(ctx, p.client, http.MethodPost, p.apiURL("/channels/"+chatID+"/messages"), p.headers(), map[string]any{"content": content, "message_reference": map[string]string{"message_id": messageID}}, &value); err != nil {
		return "", err
	}
	if value.Code != 0 {
		return "", fmt.Errorf("Discord rejected reply: %d", value.Code)
	}
	return value.ID, nil
}
func (p *discordProvider) GetGroupMessages(ctx context.Context, chatID string, count int) ([]ChannelMessage, error) {
	if count <= 0 || count > 100 {
		count = 20
	}
	var values []map[string]any
	if err := doJSON(ctx, p.client, http.MethodGet, p.apiURL("/channels/"+chatID+"/messages?limit="+fmt.Sprint(count)), p.headers(), nil, &values); err != nil {
		return nil, err
	}
	result := make([]ChannelMessage, 0, len(values))
	for _, value := range values {
		data, _ := json.Marshal(value)
		if message, ok := parseIncoming(ChannelTypeDiscord, data); ok {
			message.InstanceID = p.instance.ID
			message.Role = "context"
			result = append(result, message)
		}
	}
	return result, nil
}
func (p *discordProvider) ListGroups(ctx context.Context) ([]ChannelGroup, error) {
	var values []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := doJSON(ctx, p.client, http.MethodGet, p.apiURL("/users/@me/guilds"), p.headers(), nil, &values); err != nil {
		return nil, err
	}
	result := make([]ChannelGroup, 0, len(values))
	for _, value := range values {
		result = append(result, ChannelGroup{ID: value.ID, Name: value.Name})
	}
	return result, nil
}
func (p *discordProvider) SendMedia(ctx context.Context, chatID string, media ChannelMedia, caption string) (string, error) {
	if p.wsURL != "" {
		return p.relayMedia(ctx, chatID, media, caption)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("payload_json", `{"content":`+strconvQuote(caption)+`}`)
	part, err := writer.CreateFormFile("files[0]", media.FileName)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(media.Data); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL("/channels/"+chatID+"/messages"), &body)
	if err != nil {
		return "", err
	}
	for key, value := range p.headers() {
		request.Header.Set(key, value)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := p.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("Discord media request failed: %s", response.Status)
	}
	var value struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		return "", err
	}
	return value.ID, nil
}
func (p *discordProvider) SupportsStreaming() bool { return false }
func (p *discordProvider) SendStreamingMessage(context.Context, string, string, string) (StreamingHandle, error) {
	return nil, errors.New("Discord streaming is not supported")
}

func strconvQuote(value string) string { data, _ := json.Marshal(value); return string(data) }
