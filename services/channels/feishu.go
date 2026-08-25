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
)

type feishuProvider struct {
	*relayProvider
	appID, appSecret, token string
	apiBaseURL              string
	tokenExpires            int64
	client                  *http.Client
	tokenMu                 sync.Mutex
	cardSeq                 sync.Map
}

func newFeishuProvider(instance ChannelInstance, notify EventSink) (ChannelProvider, error) {
	appID := strings.TrimSpace(instance.Config["appId"])
	secret := strings.TrimSpace(instance.Config["appSecret"])
	if appID == "" || secret == "" {
		return nil, errors.New("Feishu appId and appSecret are required")
	}
	apiBaseURL := strings.TrimRight(strings.TrimSpace(instance.Config["apiBaseUrl"]), "/")
	if apiBaseURL == "" {
		apiBaseURL = "https://open.feishu.cn"
	}
	return &feishuProvider{relayProvider: newRelayProvider(instance, notify), appID: appID, appSecret: secret, apiBaseURL: apiBaseURL, client: channelHTTPClient()}, nil
}
func (p *feishuProvider) ensureToken(ctx context.Context) (string, error) {
	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()
	if p.token != "" && nowMillis() < p.tokenExpires {
		return p.token, nil
	}
	var value struct {
		Code   int    `json:"code"`
		Msg    string `json:"msg"`
		Token  string `json:"tenant_access_token"`
		Expire int64  `json:"expire"`
	}
	if err := doJSON(ctx, p.client, http.MethodPost, "https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal", nil, map[string]string{"app_id": p.appID, "app_secret": p.appSecret}, &value); err != nil {
		return "", err
	}
	if value.Code != 0 || value.Token == "" {
		return "", fmt.Errorf("Feishu authentication rejected")
	}
	p.token = value.Token
	expires := value.Expire
	if expires <= 60 {
		expires = 3600
	}
	p.tokenExpires = nowMillis() + (expires-60)*1000
	return p.token, nil
}
func (p *feishuProvider) auth(ctx context.Context) (map[string]string, error) {
	token, err := p.ensureToken(ctx)
	if err != nil {
		return nil, err
	}
	return stringMap("Authorization", "Bearer "+token), nil
}
func (p *feishuProvider) Start(ctx context.Context) error {
	if _, err := p.ensureToken(ctx); err != nil {
		return err
	}
	if p.wsURL != "" {
		return p.startRelay(ctx)
	}
	var value struct {
		Code int `json:"code"`
		Data struct {
			URL string `json:"URL"`
		} `json:"data"`
	}
	if err := doJSON(ctx, p.client, http.MethodPost, "https://open.feishu.cn/callback/ws/endpoint", nil, map[string]string{"AppID": p.appID, "AppSecret": p.appSecret}, &value); err == nil && value.Data.URL != "" {
		client, connectErr := newWSClient(ctx, value.Data.URL, func(raw []byte) { p.handleFeishuRaw(raw) }, func(connectErr error) { p.emit("error", connectErr.Error()) })
		if connectErr != nil {
			return connectErr
		}
		p.mu.Lock()
		p.ws = client
		p.running = true
		p.mu.Unlock()
		return nil
	}
	p.mu.Lock()
	p.running = true
	p.mu.Unlock()
	return nil
}
func (p *feishuProvider) handleFeishuRaw(raw []byte)     { p.handleRaw(raw) }
func (p *feishuProvider) Stop(ctx context.Context) error { return p.stopRelay(ctx) }
func (p *feishuProvider) IsRunning() bool                { return p.isRunning() }
func (p *feishuProvider) SendMessage(ctx context.Context, chatID, content string) (string, error) {
	if p.wsURL != "" {
		return p.relaySend(ctx, "send_message", chatID, content, ChannelMedia{})
	}
	headers, err := p.auth(ctx)
	if err != nil {
		return "", err
	}
	var value struct {
		Code int `json:"code"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	body := map[string]any{"receive_id": chatID, "msg_type": "text", "content": mustJSON(map[string]string{"text": content})}
	if err := doJSON(ctx, p.client, http.MethodPost, "https://open.feishu.cn/open-apis/im/v1/messages?receive_id_type=chat_id", headers, body, &value); err != nil {
		return "", err
	}
	if value.Code != 0 {
		return "", errors.New("Feishu rejected sendMessage")
	}
	return value.Data.MessageID, nil
}
func (p *feishuProvider) ReplyMessage(ctx context.Context, messageID, content string) (string, error) {
	if p.wsURL != "" {
		chatID := p.relayReplyChat(messageID)
		return p.relaySend(ctx, "reply_message", chatID, content, ChannelMedia{})
	}
	headers, err := p.auth(ctx)
	if err != nil {
		return "", err
	}
	var value struct {
		Code int `json:"code"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	body := map[string]any{"msg_type": "text", "content": mustJSON(map[string]string{"text": content})}
	if err := doJSON(ctx, p.client, http.MethodPost, "https://open.feishu.cn/open-apis/im/v1/messages/"+chatIDPath(messageID)+"/reply", headers, body, &value); err != nil {
		return "", err
	}
	if value.Code != 0 {
		return "", errors.New("Feishu rejected reply")
	}
	return value.Data.MessageID, nil
}
func (p *feishuProvider) GetGroupMessages(ctx context.Context, chatID string, count int) ([]ChannelMessage, error) {
	headers, err := p.auth(ctx)
	if err != nil {
		return nil, err
	}
	if count <= 0 || count > 50 {
		count = 20
	}
	var value struct {
		Code int `json:"code"`
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := doJSON(ctx, p.client, http.MethodGet, "https://open.feishu.cn/open-apis/im/v1/messages?container_id_type=chat&container_id="+chatIDPath(chatID)+"&page_size="+fmt.Sprint(count), headers, nil, &value); err != nil {
		return nil, err
	}
	result := make([]ChannelMessage, 0, len(value.Data.Items))
	for _, item := range value.Data.Items {
		raw, _ := json.Marshal(item)
		if message, ok := parseIncoming(ChannelTypeFeishu, raw); ok {
			message.InstanceID = p.instance.ID
			message.Role = "context"
			result = append(result, message)
		}
	}
	return result, nil
}
func (p *feishuProvider) ListGroups(ctx context.Context) ([]ChannelGroup, error) {
	headers, err := p.auth(ctx)
	if err != nil {
		return nil, err
	}
	var value struct {
		Code int `json:"code"`
		Data struct {
			Items []struct {
				ID      string `json:"chat_id"`
				Name    string `json:"name"`
				Members int    `json:"member_count"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := doJSON(ctx, p.client, http.MethodGet, "https://open.feishu.cn/open-apis/im/v1/chats?page_size=50", headers, nil, &value); err != nil {
		return nil, err
	}
	result := make([]ChannelGroup, 0, len(value.Data.Items))
	for _, item := range value.Data.Items {
		result = append(result, ChannelGroup{ID: item.ID, Name: item.Name, MemberCount: item.Members})
	}
	return result, nil
}
func (p *feishuProvider) SendMedia(ctx context.Context, chatID string, media ChannelMedia, caption string) (string, error) {
	if p.wsURL != "" {
		return p.relayMedia(ctx, chatID, media, caption)
	}
	headers, err := p.auth(ctx)
	if err != nil {
		return "", err
	}
	resourceType := "file"
	field := "file"
	if strings.HasPrefix(media.MediaType, "image/") {
		resourceType = "image"
		field = "image"
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("image_type", "message")
	if resourceType == "file" {
		_ = writer.WriteField("file_type", "stream")
	}
	part, err := writer.CreateFormFile(field, media.FileName)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(media.Data); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://open.feishu.cn/open-apis/im/v1/"+resourceType+"s", &body)
	if err != nil {
		return "", err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := p.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("Feishu media upload failed: %s", response.Status)
	}
	var upload struct {
		Data struct {
			Key     string `json:"image_key"`
			FileKey string `json:"file_key"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&upload); err != nil {
		return "", err
	}
	key := upload.Data.Key
	if key == "" {
		key = upload.Data.FileKey
	}
	content := map[string]any{}
	msgType := resourceType
	if resourceType == "image" {
		content["image_key"] = key
	} else {
		content["file_key"] = key
		content["file_name"] = media.FileName
	}
	if caption != "" {
		content["text"] = caption
		msgType = "post"
	}
	return p.sendTyped(ctx, chatID, msgType, content)
}
func (p *feishuProvider) sendTyped(ctx context.Context, chatID, msgType string, content map[string]any) (string, error) {
	headers, err := p.auth(ctx)
	if err != nil {
		return "", err
	}
	var value struct {
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if err := doJSON(ctx, p.client, http.MethodPost, "https://open.feishu.cn/open-apis/im/v1/messages?receive_id_type=chat_id", headers, map[string]any{"receive_id": chatID, "msg_type": msgType, "content": mustJSON(content)}, &value); err != nil {
		return "", err
	}
	return value.Data.MessageID, nil
}
func (p *feishuProvider) SupportsStreaming() bool { return p.instance.Features.StreamingReply }
func (p *feishuProvider) SendStreamingMessage(ctx context.Context, chatID, initial, replyTo string) (StreamingHandle, error) {
	if p.wsURL != "" {
		return p.relayStreaming(ctx, chatID, initial, replyTo)
	}
	headers, err := p.auth(ctx)
	if err != nil {
		return nil, err
	}
	var value struct {
		Data struct {
			CardID string `json:"card_id"`
		} `json:"data"`
	}
	card := map[string]any{"schema": "2.0", "config": map[string]any{"update_multi": true, "streaming_mode": true}, "header": map[string]any{"title": map[string]string{"tag": "plain_text", "content": "AI Assistant"}}, "body": map[string]any{"elements": []map[string]string{{"tag": "markdown", "content": initial}}}}
	if err := doJSON(ctx, p.client, http.MethodPost, "https://open.feishu.cn/open-apis/cardkit/v1/cards", headers, map[string]any{"type": "card_json", "data": mustJSON(card)}, &value); err != nil {
		return nil, err
	}
	if value.Data.CardID == "" {
		return nil, errors.New("Feishu card ID is empty")
	}
	messageID, err := p.sendTyped(ctx, chatID, "interactive", map[string]any{"type": "card", "data": map[string]string{"card_id": value.Data.CardID}})
	if err != nil {
		return nil, err
	}
	return &feishuStreamingHandle{provider: p, cardID: value.Data.CardID, messageID: messageID, sequence: 0}, nil
}

type feishuStreamingHandle struct {
	provider  *feishuProvider
	cardID    string
	messageID string
	sequence  int64
	mu        sync.Mutex
	finished  bool
}

func (h *feishuStreamingHandle) MessageID() string {
	if h == nil {
		return ""
	}
	return h.messageID
}

func (h *feishuStreamingHandle) Update(ctx context.Context, content string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.finished {
		return nil
	}
	h.sequence++
	return h.provider.updateCard(ctx, h.cardID, content, h.sequence)
}
func (h *feishuStreamingHandle) Finish(ctx context.Context, content string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.finished {
		return nil
	}
	h.finished = true
	h.sequence++
	return h.provider.updateCard(ctx, h.cardID, content, h.sequence)
}
func (p *feishuProvider) updateCard(ctx context.Context, cardID, content string, sequence int64) error {
	headers, err := p.auth(ctx)
	if err != nil {
		return err
	}
	card := map[string]any{"schema": "2.0", "config": map[string]any{"update_multi": true, "streaming_mode": true}, "header": map[string]any{"title": map[string]string{"tag": "plain_text", "content": "AI Assistant"}}, "body": map[string]any{"elements": []map[string]string{{"tag": "markdown", "content": content}}}}
	return doJSON(ctx, p.client, http.MethodPut, "https://open.feishu.cn/open-apis/cardkit/v1/cards/"+cardID, headers, map[string]any{"card": map[string]any{"type": "card_json", "data": mustJSON(card)}, "sequence": sequence}, nil)
}
func mustJSON(value any) string      { data, _ := json.Marshal(value); return string(data) }
func chatIDPath(value string) string { return strings.TrimSpace(value) }
