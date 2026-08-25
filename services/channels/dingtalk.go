package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

type dingtalkProvider struct {
	*relayProvider
	appKey, appSecret, token string
	tokenExpires             int64
	client                   *http.Client
	tokenMu                  sync.Mutex
	webhooks                 map[string]string
	cardSeq                  int64
}

func newDingTalkProvider(instance ChannelInstance, notify EventSink) (ChannelProvider, error) {
	key := strings.TrimSpace(instance.Config["appKey"])
	secret := strings.TrimSpace(instance.Config["appSecret"])
	if key == "" || secret == "" {
		return nil, errors.New("DingTalk appKey and appSecret are required")
	}
	return &dingtalkProvider{relayProvider: newRelayProvider(instance, notify), appKey: key, appSecret: secret, client: channelHTTPClient(), webhooks: map[string]string{}}, nil
}
func (p *dingtalkProvider) ensureToken(ctx context.Context) (string, error) {
	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()
	if p.token != "" && nowMillis() < p.tokenExpires {
		return p.token, nil
	}
	var value struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int64  `json:"expireIn"`
	}
	if err := doJSON(ctx, p.client, http.MethodPost, "https://api.dingtalk.com/v1.0/oauth2/accessToken", nil, map[string]string{"appKey": p.appKey, "appSecret": p.appSecret}, &value); err != nil {
		return "", err
	}
	if value.AccessToken == "" {
		return "", errors.New("DingTalk access token is empty")
	}
	p.token = value.AccessToken
	expires := value.ExpireIn
	if expires <= 60 {
		expires = 7200
	}
	p.tokenExpires = nowMillis() + (expires-60)*1000
	return p.token, nil
}
func (p *dingtalkProvider) auth(ctx context.Context) (map[string]string, error) {
	token, err := p.ensureToken(ctx)
	if err != nil {
		return nil, err
	}
	return stringMap("x-acs-dingtalk-access-token", token), nil
}
func (p *dingtalkProvider) Start(ctx context.Context) error {
	if _, err := p.ensureToken(ctx); err != nil {
		return err
	}
	if p.wsURL != "" {
		return p.startRelay(ctx)
	}
	headers, _ := p.auth(ctx)
	var value struct {
		Endpoint string `json:"endpoint"`
		Ticket   string `json:"ticket"`
	}
	if err := doJSON(ctx, p.client, http.MethodPost, "https://api.dingtalk.com/v1.0/gateway/connections/open", headers, map[string]string{"clientId": p.appKey, "clientSecret": p.appSecret}, &value); err != nil {
		return err
	}
	if value.Endpoint == "" {
		return errors.New("DingTalk gateway endpoint is empty")
	}
	endpoint := value.Endpoint
	if value.Ticket != "" {
		separator := "?"
		if strings.Contains(endpoint, "?") {
			separator = "&"
		}
		endpoint += separator + "ticket=" + url.QueryEscape(value.Ticket)
	}
	client, err := newWSClient(ctx, endpoint, func(raw []byte) { p.handleRawDingTalk(raw) }, func(connectErr error) { p.emit("error", connectErr.Error()) })
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.ws = client
	p.running = true
	p.mu.Unlock()
	return nil
}
func (p *dingtalkProvider) handleRawDingTalk(raw []byte) {
	value := decodeJSONObject(raw)
	if value != nil {
		payload := value
		if data, ok := value["data"].(string); ok {
			var nested map[string]any
			if json.Unmarshal([]byte(data), &nested) == nil {
				payload = nested
			}
		}
		chatID := firstString(payload, "conversationId", "conversation_id")
		if webhook := firstString(payload, "sessionWebhook"); webhook != "" && chatID != "" {
			p.mu.Lock()
			p.webhooks[chatID] = webhook
			p.mu.Unlock()
		}
	}
	p.handleRaw(raw)
}
func (p *dingtalkProvider) Stop(ctx context.Context) error { return p.stopRelay(ctx) }
func (p *dingtalkProvider) IsRunning() bool                { return p.isRunning() }
func (p *dingtalkProvider) SendMessage(ctx context.Context, chatID, content string) (string, error) {
	if p.wsURL != "" {
		return p.relaySend(ctx, "send_message", chatID, content, ChannelMedia{})
	}
	p.mu.RLock()
	webhook := p.webhooks[chatID]
	p.mu.RUnlock()
	if webhook != "" {
		var value map[string]any
		if err := doJSON(ctx, p.client, http.MethodPost, webhook, nil, map[string]any{"msgtype": "text", "text": map[string]string{"content": content}}, &value); err == nil {
			return "", nil
		}
	}
	headers, err := p.auth(ctx)
	if err != nil {
		return "", err
	}
	var value struct {
		ProcessQueryKey string `json:"processQueryKey"`
	}
	body := map[string]any{"msgParam": mustJSON(map[string]string{"content": content}), "msgKey": "sampleText", "openConversationId": chatID, "robotCode": p.appKey}
	if err := doJSON(ctx, p.client, http.MethodPost, "https://api.dingtalk.com/v1.0/robot/groupMessages/send", headers, body, &value); err != nil {
		return "", err
	}
	return value.ProcessQueryKey, nil
}
func (p *dingtalkProvider) ReplyMessage(ctx context.Context, messageID, content string) (string, error) {
	chatID := p.relayReplyChat(messageID)
	if chatID == "" {
		return "", fmt.Errorf("DingTalk reply context not found for %s", messageID)
	}
	return p.SendMessage(ctx, chatID, content)
}
func (p *dingtalkProvider) GetGroupMessages(context.Context, string, int) ([]ChannelMessage, error) {
	return []ChannelMessage{}, nil
}
func (p *dingtalkProvider) ListGroups(ctx context.Context) ([]ChannelGroup, error) {
	headers, err := p.auth(ctx)
	if err != nil {
		return nil, err
	}
	var value struct {
		Groups []struct {
			ID          string `json:"openConversationId"`
			Name        string `json:"name"`
			MemberCount int    `json:"memberCount"`
		} `json:"groups"`
	}
	if err := doJSON(ctx, p.client, http.MethodPost, "https://api.dingtalk.com/v1.0/robot/groups/lists", headers, map[string]any{"robotCode": p.appKey, "statusCode": 0, "maxResults": 50}, &value); err != nil {
		return nil, err
	}
	result := make([]ChannelGroup, 0, len(value.Groups))
	for _, group := range value.Groups {
		result = append(result, ChannelGroup{ID: group.ID, Name: group.Name, MemberCount: group.MemberCount})
	}
	return result, nil
}
func (p *dingtalkProvider) SendMedia(ctx context.Context, chatID string, media ChannelMedia, caption string) (string, error) {
	if p.wsURL != "" {
		return p.relayMedia(ctx, chatID, media, caption)
	}
	return "", errors.New("DingTalk media delivery requires a configured relay")
}
func (p *dingtalkProvider) SupportsStreaming() bool {
	return strings.TrimSpace(p.instance.Config["cardTemplateId"]) != "" && p.instance.Features.StreamingReply
}
func (p *dingtalkProvider) SendStreamingMessage(ctx context.Context, chatID, initial, replyTo string) (StreamingHandle, error) {
	template := strings.TrimSpace(p.instance.Config["cardTemplateId"])
	if template == "" {
		return nil, errors.New("DingTalk cardTemplateId is not configured")
	}
	headers, err := p.auth(ctx)
	if err != nil {
		return nil, err
	}
	p.cardSeq++
	track := fmt.Sprintf("%s-%d", p.instance.ID, p.cardSeq)
	meta := map[string]any{"cardTemplateId": template, "outTrackId": track, "openSpaceId": "dtv1.card//IM_GROUP." + chatID, "callbackType": "STREAM", "cardData": map[string]any{"cardParamMap": map[string]string{"content": initial}}, "imGroupOpenDeliverModel": map[string]string{"robotCode": p.appKey}, "imGroupOpenSpaceModel": map[string]bool{"supportForward": true}}
	if err := doJSON(ctx, p.client, http.MethodPost, "https://api.dingtalk.com/v1.0/card/instances/createAndDeliver", headers, meta, nil); err != nil {
		return nil, err
	}
	return &dingtalkStreamingHandle{provider: p, track: track, sequence: 0}, nil
}

type dingtalkStreamingHandle struct {
	provider *dingtalkProvider
	track    string
	sequence int64
	mu       sync.Mutex
	finished bool
}

func (h *dingtalkStreamingHandle) MessageID() string { return "" }

func (h *dingtalkStreamingHandle) Update(ctx context.Context, content string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.finished {
		return nil
	}
	h.sequence++
	return h.provider.updateCard(ctx, h.track, content, h.sequence, false)
}
func (h *dingtalkStreamingHandle) Finish(ctx context.Context, content string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.finished {
		return nil
	}
	h.finished = true
	h.sequence++
	return h.provider.updateCard(ctx, h.track, content, h.sequence, true)
}
func (p *dingtalkProvider) updateCard(ctx context.Context, track, content string, sequence int64, final bool) error {
	headers, err := p.auth(ctx)
	if err != nil {
		return err
	}
	return doJSON(ctx, p.client, http.MethodPut, "https://api.dingtalk.com/v1.0/card/streaming", headers, map[string]any{"outTrackId": track, "guid": fmt.Sprintf("%s-%d", track, sequence), "key": "content", "content": content, "isFull": true, "isFinalize": final}, nil)
}
