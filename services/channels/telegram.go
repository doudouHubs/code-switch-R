package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"
)

type telegramProvider struct {
	*relayProvider
	token  string
	client *http.Client
	cancel context.CancelFunc
	wg     sync.WaitGroup
	offset int64
}

func newTelegramProvider(instance ChannelInstance, notify EventSink) (ChannelProvider, error) {
	if strings.TrimSpace(instance.Config["botToken"]) == "" {
		return nil, errors.New("Telegram botToken is required")
	}
	return &telegramProvider{relayProvider: newRelayProvider(instance, notify), token: strings.TrimSpace(instance.Config["botToken"]), client: channelHTTPClient()}, nil
}

func (p *telegramProvider) apiURL(method string) string {
	return "https://api.telegram.org/bot" + p.token + "/" + method
}

func (p *telegramProvider) Start(ctx context.Context) error {
	if p.wsURL != "" {
		return p.startRelay(ctx)
	}
	if err := doJSON(ctx, p.client, http.MethodGet, p.apiURL("getMe"), nil, nil, nil); err != nil {
		return err
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

func (p *telegramProvider) poll(ctx context.Context) {
	defer p.wg.Done()
	for ctx.Err() == nil {
		var response struct {
			OK     bool             `json:"ok"`
			Result []map[string]any `json:"result"`
		}
		endpoint := p.apiURL("getUpdates") + "?timeout=25&allowed_updates=%5B%22message%22%2C%22edited_message%22%5D"
		if p.offset > 0 {
			endpoint += "&offset=" + strconv.FormatInt(p.offset, 10)
		}
		if err := doJSON(ctx, p.client, http.MethodGet, endpoint, nil, nil, &response); err != nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(2 * time.Second)
			continue
		}
		for _, update := range response.Result {
			if value, ok := update["update_id"].(float64); ok {
				p.offset = int64(value) + 1
			}
			raw, _ := json.Marshal(update)
			p.handleTelegramUpdate(raw)
		}
	}
}

func (p *telegramProvider) handleTelegramUpdate(raw []byte) {
	value := decodeJSONObject(raw)
	if value == nil {
		return
	}
	msgValue, ok := value["message"].(map[string]any)
	if !ok {
		msgValue, _ = value["edited_message"].(map[string]any)
	}
	if msgValue == nil {
		return
	}
	chat, _ := msgValue["chat"].(map[string]any)
	from, _ := msgValue["from"].(map[string]any)
	chatID := firstString(chat, "id")
	external := firstString(msgValue, "message_id")
	content := firstString(msgValue, "text", "caption")
	if content == "" {
		if _, ok := msgValue["photo"]; ok {
			content = "[User sent an image]"
		} else if _, ok := msgValue["audio"]; ok {
			content = "[User sent an audio message]"
		} else if _, ok := msgValue["document"]; ok {
			content = "[User sent a file]"
		}
	}
	if chatID == "" || external == "" || content == "" {
		return
	}
	timestamp := int64(0)
	if rawDate, ok := msgValue["date"].(float64); ok {
		timestamp = int64(rawDate) * 1000
	}
	if timestamp == 0 {
		timestamp = nowMillis()
	}
	message := ChannelMessage{InstanceID: p.instance.ID, ExternalID: external, Role: "user", ChatID: chatID, SenderID: firstString(from, "id"), SenderName: strings.TrimSpace(firstString(from, "first_name", "username") + " " + firstString(from, "last_name")), Content: content, Timestamp: timestamp, Raw: string(raw)}
	p.messageChats[external] = chatID
	p.emit("incoming_message", message)
}

func (p *telegramProvider) Stop(ctx context.Context) error {
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
		p.wg.Wait()
	}
	return p.stopRelay(ctx)
}
func (p *telegramProvider) IsRunning() bool { return p.isRunning() }
func (p *telegramProvider) SendMessage(ctx context.Context, chatID, content string) (string, error) {
	if p.wsURL != "" {
		return p.relaySend(ctx, "send_message", chatID, content, ChannelMedia{})
	}
	var response struct {
		OK     bool `json:"ok"`
		Result struct {
			ID int64 `json:"message_id"`
		} `json:"result"`
	}
	if err := doJSON(ctx, p.client, http.MethodPost, p.apiURL("sendMessage"), nil, map[string]any{"chat_id": chatID, "text": content}, &response); err != nil {
		return "", err
	}
	if !response.OK {
		return "", errors.New("Telegram rejected sendMessage")
	}
	return strconv.FormatInt(response.Result.ID, 10), nil
}
func (p *telegramProvider) ReplyMessage(ctx context.Context, messageID, content string) (string, error) {
	chatID := p.relayReplyChat(messageID)
	if chatID == "" {
		return "", errors.New("Telegram reply context not found")
	}
	body := map[string]any{"chat_id": chatID, "text": content}
	if id, err := strconv.ParseInt(messageID, 10, 64); err == nil {
		body["reply_parameters"] = map[string]any{"message_id": id}
	}
	var response struct {
		OK     bool `json:"ok"`
		Result struct {
			ID int64 `json:"message_id"`
		} `json:"result"`
	}
	if err := doJSON(ctx, p.client, http.MethodPost, p.apiURL("sendMessage"), nil, body, &response); err != nil {
		return "", err
	}
	if !response.OK {
		return "", errors.New("Telegram rejected reply")
	}
	return strconv.FormatInt(response.Result.ID, 10), nil
}
func (p *telegramProvider) GetGroupMessages(context.Context, string, int) ([]ChannelMessage, error) {
	return []ChannelMessage{}, nil
}
func (p *telegramProvider) ListGroups(context.Context) ([]ChannelGroup, error) {
	return []ChannelGroup{}, nil
}
func (p *telegramProvider) SendMedia(ctx context.Context, chatID string, media ChannelMedia, caption string) (string, error) {
	if p.wsURL != "" {
		return p.relayMedia(ctx, chatID, media, caption)
	}
	var field string
	switch {
	case strings.HasPrefix(media.MediaType, "image/"):
		field = "photo"
	case media.Kind == "audio":
		field = "audio"
	default:
		field = "document"
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("chat_id", chatID)
	if caption != "" {
		_ = writer.WriteField("caption", caption)
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="`+field+`"; filename="`+media.FileName+`"`)
	header.Set("Content-Type", media.MediaType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(media.Data); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL("send"+strings.Title(field)), &body)
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := p.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("Telegram media request failed: %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, channelMaxHTTPBody))
	if err != nil {
		return "", err
	}
	var value struct {
		OK     bool `json:"ok"`
		Result struct {
			ID int64 `json:"message_id"`
		} `json:"result"`
	}
	if json.Unmarshal(data, &value) != nil || !value.OK {
		return "", errors.New("Telegram rejected media")
	}
	return strconv.FormatInt(value.Result.ID, 10), nil
}
func (p *telegramProvider) SupportsStreaming() bool { return false }
func (p *telegramProvider) SendStreamingMessage(context.Context, string, string, string) (StreamingHandle, error) {
	return nil, errors.New("Telegram streaming is not supported")
}
