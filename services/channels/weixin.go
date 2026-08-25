package channels

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type weixinProvider struct {
	*relayProvider
	baseURL, cdnBaseURL, routeTag, token, accountID, uin string
	client                                             *http.Client
	cancel                                             context.CancelFunc
	wg                                                 sync.WaitGroup
	syncBuf                                            string
	contextTokens                                      map[string]string
	messageTokens                                      map[string]string
}

func newWeixinProvider(instance ChannelInstance, notify EventSink) (ChannelProvider, error) {
	token := strings.TrimSpace(instance.Config["token"])
	account := strings.TrimSpace(instance.Config["accountId"])
	if token == "" || account == "" {
		return nil, errors.New("Weixin token and accountId are required")
	}
	base := strings.TrimRight(strings.TrimSpace(instance.Config["baseUrl"]), "/")
	if base == "" {
		base = "https://ilinkai.weixin.qq.com"
	}
	cdnBase := strings.TrimRight(strings.TrimSpace(instance.Config["cdnBaseUrl"]), "/")
	if cdnBase == "" {
		cdnBase = "https://novac2c.cdn.weixin.qq.com/c2c"
	}
	uinBytes := make([]byte, 4)
	_, _ = rand.Read(uinBytes)
	uin := base64.StdEncoding.EncodeToString([]byte(fmt.Sprint(uint32(uinBytes[0])<<24 | uint32(uinBytes[1])<<16 | uint32(uinBytes[2])<<8 | uint32(uinBytes[3]))))
	return &weixinProvider{relayProvider: newRelayProvider(instance, notify), baseURL: base, cdnBaseURL: cdnBase, routeTag: strings.TrimSpace(instance.Config["routeTag"]), token: token, accountID: account, uin: uin, client: &http.Client{Timeout: 45 * time.Second}, contextTokens: map[string]string{}, messageTokens: map[string]string{}}, nil
}
func (p *weixinProvider) headers() map[string]string {
	headers := stringMap("Content-Type", "application/json", "AuthorizationType", "ilink_bot_token", "Authorization", "Bearer "+p.token, "X-WECHAT-UIN", p.uin)
	if p.routeTag != "" {
		headers["SKRouteTag"] = p.routeTag
	}
	return headers
}
func (p *weixinProvider) post(ctx context.Context, path string, body any, response any) error {
	return doJSON(ctx, p.client, http.MethodPost, p.baseURL+"/"+path, p.headers(), body, response)
}
func (p *weixinProvider) Start(ctx context.Context) error {
	if p.wsURL != "" {
		return p.startRelay(ctx)
	}
	p.mu.Lock()
	p.running = true
	p.mu.Unlock()
	loopCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.wg.Add(1)
	go p.poll(loopCtx)
	return nil
}
func (p *weixinProvider) poll(ctx context.Context) {
	defer p.wg.Done()
	for ctx.Err() == nil {
		var response struct {
			Ret     int              `json:"ret"`
			ErrCode int              `json:"errcode"`
			Msgs    []map[string]any `json:"msgs"`
			Buffer  string           `json:"get_updates_buf"`
			Timeout int              `json:"longpolling_timeout_ms"`
			ErrMsg  string           `json:"errmsg"`
		}
		if err := p.post(ctx, "ilink/bot/getupdates", map[string]string{"get_updates_buf": p.syncBuf}, &response); err != nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(2 * time.Second)
			continue
		}
		if response.Buffer != "" {
			p.syncBuf = response.Buffer
		}
		for _, raw := range response.Msgs {
			p.handleWeixinMessage(ctx, raw)
		}
		if response.Timeout <= 0 {
			response.Timeout = 35000
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func (p *weixinProvider) handleWeixinMessage(ctx context.Context, value map[string]any) {
	if typ := int(firstNumber(value, "message_type")); typ != 0 && typ != 1 {
		return
	}
	user := firstString(value, "from_user_id")
	if user == "" {
		return
	}
	items, _ := value["item_list"].([]any)
	content := ""
	kind := "text"
	var media *ChannelMedia
	for _, rawItem := range items {
		item, _ := rawItem.(map[string]any)
		typ := int(firstNumber(item, "type"))
		switch typ {
		case 1:
			if nested, ok := item["text_item"].(map[string]any); ok {
				content = firstString(nested, "text")
			}
		case 3:
			kind = "audio"
			if nested, ok := item["voice_item"].(map[string]any); ok {
				content = firstString(nested, "text")
			}
			if content == "" {
				content = "[User sent an audio message]"
			}
		case 2:
			kind = "image"
			content = "[User sent an image]"
			if nested, ok := item["image_item"].(map[string]any); ok {
				fileID := firstString(nested, "file_id")
				if fileID != "" {
					if data, mediaType, err := p.downloadImage(ctx, firstString(value, "message_id", "client_id"), nested); err == nil {
						media = &ChannelMedia{Kind: "image", MediaType: mediaType, Data: data, FileName: firstString(nested, "file_name")}
					}
				}
			}
		case 4:
			kind = "file"
			content = "[User sent a file]"
		}
	}
	if content == "" {
		return
	}
	external := firstString(value, "message_id", "client_id")
	if external == "" {
		external = fmt.Sprintf("%s-%d", user, nowMillis())
	}
	contextToken := firstString(value, "context_token")
	if contextToken != "" {
		p.mu.Lock()
		p.contextTokens[user] = contextToken
		p.messageTokens[external] = contextToken
		p.messageChats[external] = user
		p.mu.Unlock()
	}
	message := ChannelMessage{InstanceID: p.instance.ID, ExternalID: external, Role: "user", ChatID: user, SenderID: user, SenderName: user, Content: content, Timestamp: firstNumber(value, "create_time_ms"), Raw: mustJSON(value)}
	if message.Timestamp == 0 {
		message.Timestamp = nowMillis()
	}
	if media != nil {
		if kind == "image" {
			message.Images = []ChannelMedia{*media}
		} else {
			message.Audio = media
		}
	}
	p.emit("incoming_message", message)
}
func (p *weixinProvider) downloadImage(ctx context.Context, messageID string, item map[string]any) ([]byte, string, error) {
	body := map[string]any{"message_id": messageID, "file_id": firstString(item, "file_id"), "aes_key": firstString(item, "aes_key", "aeskey"), "md5sum": firstString(item, "md5sum"), "file_name": firstString(item, "file_name")}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/ilink/bot/downloadmessageimage", mustReader(body))
	if err != nil {
		return nil, "", err
	}
	for key, value := range p.headers() {
		request.Header.Set(key, value)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", fmt.Errorf("Weixin image download failed: %s", response.Status)
	}
	data, err := ioReadLimit(response.Body, channelMaxHTTPBody)
	return data, response.Header.Get("Content-Type"), err
}
func (p *weixinProvider) Stop(ctx context.Context) error {
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
		p.wg.Wait()
	}
	return p.stopRelay(ctx)
}
func (p *weixinProvider) IsRunning() bool { return p.isRunning() }
func (p *weixinProvider) contextToken(chatID string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.contextTokens[chatID]
}
func (p *weixinProvider) SendMessage(ctx context.Context, chatID, content string) (string, error) {
	if p.wsURL != "" {
		return p.relaySend(ctx, "send_message", chatID, content, ChannelMedia{})
	}
	token := p.contextToken(chatID)
	if token == "" {
		return "", errors.New("Weixin reply context is not available")
	}
	clientID := fmt.Sprintf("%d", nowMillis())
	var response map[string]any
	body := map[string]any{"msg": map[string]any{"from_user_id": "", "to_user_id": chatID, "client_id": clientID, "message_type": 2, "message_state": 2, "item_list": []map[string]any{{"type": 1, "text_item": map[string]string{"text": content}}}, "context_token": token}}
	if err := p.post(ctx, "ilink/bot/sendmessage", body, &response); err != nil {
		return "", err
	}
	if value := firstString(response, "errcode", "ret"); value != "" && value != "0" {
		return "", errors.New("Weixin rejected sendMessage")
	}
	return clientID, nil
}
func (p *weixinProvider) ReplyMessage(ctx context.Context, messageID, content string) (string, error) {
	p.mu.RLock()
	chatID := p.messageChats[messageID]
	p.mu.RUnlock()
	if chatID == "" {
		return "", errors.New("Weixin reply context not found")
	}
	return p.SendMessage(ctx, chatID, content)
}
func (p *weixinProvider) GetGroupMessages(context.Context, string, int) ([]ChannelMessage, error) {
	return []ChannelMessage{}, nil
}
func (p *weixinProvider) ListGroups(context.Context) ([]ChannelGroup, error) {
	return []ChannelGroup{}, nil
}
func (p *weixinProvider) SendMedia(ctx context.Context, chatID string, media ChannelMedia, caption string) (string, error) {
	if p.wsURL != "" {
		return p.relayMedia(ctx, chatID, media, caption)
	}
	if strings.HasPrefix(strings.ToLower(media.MediaType), "image/") || media.Kind == "image" {
		return p.SendWeixinImage(ctx, chatID, media, caption)
	}
	return p.SendWeixinFile(ctx, chatID, media, caption)
}
func (p *weixinProvider) SupportsStreaming() bool { return false }
func (p *weixinProvider) SendStreamingMessage(context.Context, string, string, string) (StreamingHandle, error) {
	return nil, errors.New("Weixin streaming is not supported")
}

func mustReader(value any) *bytes.Reader {
	data, _ := json.Marshal(value)
	return bytes.NewReader(data)
}
func ioReadLimit(reader io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(reader, limit+1))
}
