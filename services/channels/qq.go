package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type qqProvider struct {
	*relayProvider
	appID           string
	clientSecret    string
	apiBase         string
	client          *http.Client
	heartbeatCancel context.CancelFunc
	heartbeat       sync.WaitGroup
}

func newQQProvider(instance ChannelInstance, notify EventSink) (ChannelProvider, error) {
	appID := strings.TrimSpace(instance.Config["appId"])
	secret := strings.TrimSpace(instance.Config["clientSecret"])
	if appID == "" || secret == "" {
		return nil, errors.New("QQ appId and clientSecret are required")
	}
	base := "https://api.sgroup.qq.com"
	if strings.EqualFold(strings.TrimSpace(instance.Config["useSandbox"]), "true") {
		base = "https://sandbox.api.sgroup.qq.com"
	}
	return &qqProvider{relayProvider: newRelayProvider(instance, notify), appID: appID, clientSecret: secret, apiBase: base, client: channelHTTPClient()}, nil
}

func (p *qqProvider) accessToken(ctx context.Context) (string, error) {
	var value struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	err := doJSON(ctx, p.client, http.MethodPost, "https://bots.qq.com/app/getAppAccessToken", map[string]string{"Content-Type": "application/json"}, map[string]string{"appId": p.appID, "clientSecret": p.clientSecret}, &value)
	if err != nil {
		return "", err
	}
	if value.AccessToken == "" {
		return "", errors.New("QQ access token is empty")
	}
	return value.AccessToken, nil
}
func (p *qqProvider) apiRequest(ctx context.Context, method, path string, body any, response any) error {
	token, err := p.accessToken(ctx)
	if err != nil {
		return err
	}
	return doJSON(ctx, p.client, method, p.apiBase+path, map[string]string{"Authorization": "QQBot " + token}, body, response)
}
func (p *qqProvider) Start(ctx context.Context) error {
	if p.wsURL != "" {
		return p.startRelay(ctx)
	}
	gateway, err := p.gatewayURL(ctx)
	if err != nil {
		return err
	}
	client, err := newWSClient(ctx, gateway, func(raw []byte) { p.handleGateway(raw) }, func(err error) { p.emit("error", err.Error()) })
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.ws = client
	p.running = true
	p.mu.Unlock()
	return nil
}
func (p *qqProvider) gatewayURL(ctx context.Context) (string, error) {
	var value struct {
		URL string `json:"url"`
	}
	if err := p.apiRequest(ctx, http.MethodGet, "/gateway", nil, &value); err != nil {
		return "", err
	}
	if value.URL == "" {
		return "", errors.New("QQ gateway URL is empty")
	}
	return value.URL, nil
}
func (p *qqProvider) handleGateway(raw []byte) {
	value := decodeJSONObject(raw)
	if value == nil {
		return
	}
	op := int(firstNumber(value, "op"))
	switch op {
	case 10:
		d, _ := value["d"].(map[string]any)
		interval := int(firstNumber(d, "heartbeat_interval"))
		if interval <= 0 {
			interval = 45000
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
		token, err := p.accessToken(context.Background())
		if err != nil {
			p.emit("error", err.Error())
			return
		}
		_ = p.ws.WriteJSON(map[string]any{"op": 2, "d": map[string]any{"token": "QQBot " + token, "intents": (1 << 30) | (1 << 25) | (1 << 26), "shard": []int{0, 1}}})
	case 0:
		if message, ok := parseIncoming(ChannelTypeQQ, raw); ok {
			message.InstanceID = p.instance.ID
			p.messageChats[message.ExternalID] = message.ChatID
			p.emit("incoming_message", message)
		}
	}
}
func (p *qqProvider) Stop(ctx context.Context) error {
	if p.heartbeatCancel != nil {
		p.heartbeatCancel()
		p.heartbeatCancel = nil
		p.heartbeat.Wait()
	}
	return p.stopRelay(ctx)
}
func (p *qqProvider) IsRunning() bool { return p.isRunning() }
func qqTarget(chatID string) (kind, id string, err error) {
	normalized := strings.TrimSpace(strings.TrimPrefix(chatID, "qqbot:"))
	for _, prefix := range []string{"c2c:", "group:", "channel:"} {
		if strings.HasPrefix(normalized, prefix) {
			return strings.TrimSuffix(prefix, ":"), strings.TrimPrefix(normalized, prefix), nil
		}
	}
	if normalized != "" {
		return "c2c", normalized, nil
	}
	return "", "", errors.New("QQ chat ID is empty")
}
func (p *qqProvider) SendMessage(ctx context.Context, chatID, content string) (string, error) {
	if p.wsURL != "" {
		return p.relaySend(ctx, "send_message", chatID, content, ChannelMedia{})
	}
	kind, id, err := qqTarget(chatID)
	if err != nil {
		return "", err
	}
	body := map[string]any{"content": content, "msg_type": 0, "msg_seq": 1}
	path := ""
	switch kind {
	case "c2c":
		path = "/v2/users/" + url.PathEscape(id) + "/messages"
	case "group":
		path = "/v2/groups/" + url.PathEscape(id) + "/messages"
	case "channel":
		path = "/channels/" + url.PathEscape(id) + "/messages"
	default:
		return "", errors.New("unsupported QQ chat target")
	}
	var value struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	}
	if err := p.apiRequest(ctx, http.MethodPost, path, body, &value); err != nil {
		return "", err
	}
	return value.ID, nil
}
func (p *qqProvider) ReplyMessage(ctx context.Context, messageID, content string) (string, error) {
	chatID := p.relayReplyChat(messageID)
	if chatID == "" {
		if decoded, err := url.QueryUnescape(messageID); err == nil {
			chatID = decoded
		}
	}
	if chatID == "" {
		return "", errors.New("QQ reply context not found")
	}
	return p.SendMessage(ctx, chatID, content)
}
func (p *qqProvider) GetGroupMessages(context.Context, string, int) ([]ChannelMessage, error) {
	return []ChannelMessage{}, nil
}
func (p *qqProvider) ListGroups(context.Context) ([]ChannelGroup, error) {
	return []ChannelGroup{}, nil
}
func (p *qqProvider) SendMedia(ctx context.Context, chatID string, media ChannelMedia, caption string) (string, error) {
	if p.wsURL != "" {
		return p.relayMedia(ctx, chatID, media, caption)
	}
	return "", errors.New("QQ media delivery requires the provider upload API or relay")
}
func (p *qqProvider) SupportsStreaming() bool { return false }
func (p *qqProvider) SendStreamingMessage(context.Context, string, string, string) (StreamingHandle, error) {
	return nil, errors.New("QQ streaming is not supported")
}

func qqJSON(value any) string   { data, _ := json.Marshal(value); return string(data) }
func qqNumber(value int) string { return strconv.Itoa(value) }

var _ = fmt.Sprintf
var _ = qqJSON
var _ = qqNumber
