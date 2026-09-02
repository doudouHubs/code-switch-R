package channels

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newWeixinImageTestProvider(t *testing.T, serverURL string, events *[]ChannelEvent) *weixinProvider {
	t.Helper()
	instance := ChannelInstance{
		ID:   "weixin-image-test",
		Type: ChannelTypeWeixin,
		Config: map[string]string{
			"token":      "test-token",
			"accountId":  "test-account",
			"baseUrl":    serverURL,
			"cdnBaseUrl": serverURL,
		},
	}
	provider, err := newWeixinProvider(instance, func(event ChannelEvent) {
		*events = append(*events, event)
	})
	if err != nil {
		t.Fatalf("newWeixinProvider() error = %v", err)
	}
	weixin, ok := provider.(*weixinProvider)
	if !ok {
		t.Fatalf("provider type = %T", provider)
	}
	return weixin
}

func TestWeixinProviderRestoresMessageContextFromPersistedRaw(t *testing.T) {
	events := make([]ChannelEvent, 0)
	provider := newWeixinImageTestProvider(t, "http://127.0.0.1:1", &events)
	provider.RestoreMessageContext(ChannelMessage{
		ChatID:     "wx-user",
		ExternalID: "inbound-1",
		Raw:        `{"context_token":"persisted-context"}`,
	})
	if got := provider.contextToken("wx-user"); got != "persisted-context" {
		t.Fatalf("restored context token = %q", got)
	}
	provider.mu.RLock()
	gotMessageToken := provider.messageTokens["inbound-1"]
	gotMessageChat := provider.messageChats["inbound-1"]
	provider.mu.RUnlock()
	if gotMessageToken != "persisted-context" || gotMessageChat != "wx-user" {
		t.Fatalf("restored message context = token %q chat %q", gotMessageToken, gotMessageChat)
	}
}

func TestWeixinProviderSendMessagePreservesParagraphBreaks(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/ilink/bot/sendmessage" {
			t.Errorf("send message request = %s %s", request.Method, request.URL.String())
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode send message request: %v", err)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()

	provider := newWeixinImageTestProvider(t, server.URL, nil)
	provider.mu.Lock()
	provider.contextTokens["wx-user"] = "context-token"
	provider.mu.Unlock()

	if _, err := provider.SendMessage(context.Background(), "wx-user", "第一行\n\n第二行"); err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}

	message, ok := requestBody["msg"].(map[string]any)
	if !ok {
		t.Fatalf("send message body = %#v", requestBody)
	}
	items, ok := message["item_list"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("send message items = %#v", message["item_list"])
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("send message item = %#v", items[0])
	}
	textItem, ok := item["text_item"].(map[string]any)
	if !ok || textItem["text"] != "第一行\n\n第二行" {
		t.Fatalf("send message text item = %#v", item["text_item"])
	}
}

func findIncomingWeixinMessage(t *testing.T, events []ChannelEvent) ChannelMessage {
	t.Helper()
	for _, event := range events {
		if event.Type != "incoming_message" {
			continue
		}
		message, ok := event.Data.(ChannelMessage)
		if !ok {
			t.Fatalf("incoming event data type = %T", event.Data)
		}
		return message
	}
	t.Fatalf("incoming_message event not found: %#v", events)
	return ChannelMessage{}
}

func weixinImageInboundMessage(imageItem map[string]any) map[string]any {
	return map[string]any{
		"message_type":  float64(1),
		"message_id":    float64(42),
		"from_user_id":  "wx-user",
		"context_token": "context-token",
		"item_list": []any{
			map[string]any{
				"type":       float64(2),
				"image_item": imageItem,
			},
		},
	}
}

func TestWeixinProviderDownloadsCDNImageAndDecryptsRawAESKey(t *testing.T) {
	plaintext := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	}
	key := []byte("0123456789abcdef")
	ciphertext, err := encryptWeixinECB(plaintext, key)
	if err != nil {
		t.Fatalf("encryptWeixinECB() error = %v", err)
	}
	const queryParam = "cdn-image-query"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/download" {
			t.Errorf("CDN request = %s %s", request.Method, request.URL.String())
			return
		}
		if got := request.URL.Query().Get("encrypted_query_param"); got != queryParam {
			t.Errorf("encrypted_query_param = %q, want %q", got, queryParam)
		}
		writer.Header().Set("Content-Type", "application/octet-stream")
		_, _ = writer.Write(ciphertext)
	}))
	defer server.Close()

	var events []ChannelEvent
	provider := newWeixinImageTestProvider(t, server.URL, &events)
	provider.handleWeixinMessage(context.Background(), weixinImageInboundMessage(map[string]any{
		"file_name": "photo.png",
		"aeskey":    hex.EncodeToString(key),
		"media": map[string]any{
			"encrypt_query_param": queryParam,
		},
	}))

	message := findIncomingWeixinMessage(t, events)
	if message.Content != "[User sent an image]" {
		t.Fatalf("message content = %q", message.Content)
	}
	if len(message.Images) != 1 {
		t.Fatalf("message images = %#v", message.Images)
	}
	if !bytes.Equal(message.Images[0].Data, plaintext) || message.Images[0].MediaType != "image/png" {
		t.Fatalf("downloaded image = %#v", message.Images[0])
	}
	if message.Images[0].FileName != "photo.png" {
		t.Fatalf("image file name = %q", message.Images[0].FileName)
	}
}

func TestWeixinProviderDownloadsCDNImageUsingMediaAESKeyAndThumbFallback(t *testing.T) {
	plaintext := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}
	key := []byte("abcdef0123456789")
	ciphertext, err := encryptWeixinECB(plaintext, key)
	if err != nil {
		t.Fatalf("encryptWeixinECB() error = %v", err)
	}
	const queryParam = "thumb-image-query"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/download" {
			t.Errorf("request path = %q", request.URL.Path)
			return
		}
		if got := request.URL.Query().Get("encrypted_query_param"); got != queryParam {
			t.Errorf("encrypted_query_param = %q, want %q", got, queryParam)
		}
		writer.Header().Set("Content-Type", "application/octet-stream")
		_, _ = writer.Write(ciphertext)
	}))
	defer server.Close()

	var events []ChannelEvent
	provider := newWeixinImageTestProvider(t, server.URL, &events)
	provider.handleWeixinMessage(context.Background(), weixinImageInboundMessage(map[string]any{
		"file_name": "photo.jpg",
		"thumb_media": map[string]any{
			"encrypt_query_param": queryParam,
			"aes_key":             base64.StdEncoding.EncodeToString(key),
		},
	}))

	message := findIncomingWeixinMessage(t, events)
	if len(message.Images) != 1 || !bytes.Equal(message.Images[0].Data, plaintext) || message.Images[0].MediaType != "image/jpeg" {
		t.Fatalf("downloaded thumbnail image = %#v", message.Images)
	}
}

func TestWeixinProviderKeepsLegacyFileIDImageDownloadCompatible(t *testing.T) {
	plaintext := []byte{0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00}
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/ilink/bot/downloadmessageimage" {
			t.Errorf("legacy request = %s %s", request.Method, request.URL.String())
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode legacy request: %v", err)
			return
		}
		if request.Header.Get("X-WECHAT-UIN") == "" {
			t.Error("legacy request missing X-WECHAT-UIN")
		}
		writer.Header().Set("Content-Type", "application/octet-stream")
		_, _ = writer.Write(plaintext)
	}))
	defer server.Close()

	var events []ChannelEvent
	provider := newWeixinImageTestProvider(t, server.URL, &events)
	provider.handleWeixinMessage(context.Background(), weixinImageInboundMessage(map[string]any{
		"file_id":   "legacy-file-id",
		"md5sum":    "legacy-md5",
		"file_name": "legacy.gif",
		"aes_key":   "legacy-aes-key",
	}))

	message := findIncomingWeixinMessage(t, events)
	if len(message.Images) != 1 || !bytes.Equal(message.Images[0].Data, plaintext) || message.Images[0].MediaType != "image/gif" {
		t.Fatalf("legacy image = %#v", message.Images)
	}
	if got := requestBody["message_id"]; got != "42" {
		t.Fatalf("legacy message_id = %#v", got)
	}
	if got := requestBody["file_id"]; got != "legacy-file-id" {
		t.Fatalf("legacy file_id = %#v", got)
	}
}

func TestWeixinProviderReportsInboundImageDownloadFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "download failed", http.StatusBadGateway)
	}))
	defer server.Close()

	var events []ChannelEvent
	provider := newWeixinImageTestProvider(t, server.URL, &events)
	provider.handleWeixinMessage(context.Background(), weixinImageInboundMessage(map[string]any{
		"media": map[string]any{
			"encrypt_query_param": "failed-image-query",
		},
	}))

	message := findIncomingWeixinMessage(t, events)
	if !strings.Contains(message.Content, "download failed") || len(message.Images) != 0 {
		t.Fatalf("failed image message = %#v", message)
	}
	for _, event := range events {
		if event.Type == "error" && strings.Contains(event.Data.(string), "cdn-media") {
			return
		}
	}
	t.Fatalf("image download error event not found: %#v", events)
}

func TestWeixinProviderRejectsInvalidInboundImageAESKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "image/png")
		_, _ = writer.Write([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a})
	}))
	defer server.Close()

	var events []ChannelEvent
	provider := newWeixinImageTestProvider(t, server.URL, &events)
	provider.handleWeixinMessage(context.Background(), weixinImageInboundMessage(map[string]any{
		"aeskey": "not-a-hex-key",
		"media": map[string]any{
			"encrypt_query_param": "invalid-key-image",
		},
	}))

	message := findIncomingWeixinMessage(t, events)
	if !strings.Contains(message.Content, "download failed") || len(message.Images) != 0 {
		t.Fatalf("invalid key image message = %#v", message)
	}
	for _, event := range events {
		if event.Type == "error" && strings.Contains(event.Data.(string), "invalid Weixin image aeskey") {
			return
		}
	}
	t.Fatalf("invalid AES key error event not found: %#v", events)
}

func TestWeixinMediaReadLimitRejectsOversizedPayload(t *testing.T) {
	_, err := ioReadLimit(bytes.NewReader([]byte("12345")), 4)
	if err == nil || !strings.Contains(err.Error(), "exceeds 4 bytes") {
		t.Fatalf("ioReadLimit() error = %v", err)
	}
}

func TestAgentRuntimePassesChannelImageToCodex(t *testing.T) {
	chatRuntime := &channelChatRuntimeStub{}
	store, manager, instance := newChannelRuntimeFixture(t, chatRuntime)
	defer store.Close()
	defer manager.Stop(context.Background(), instance.ID)

	runtime := NewAgentRuntime(store, manager, nil, nil, AgentRuntimeOptions{ChatRuntime: chatRuntime})
	defer runtime.Close()
	imageData := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	runtime.handleIncoming(ChannelMessage{
		InstanceID: instance.ID,
		ExternalID: "incoming-image",
		Role:       "user",
		ChatID:     "chat-image",
		Content:    "[User sent an image]",
		Images:     []ChannelMedia{{Kind: "image", MediaType: "image/png; charset=binary", Data: imageData}},
		Timestamp:  nowMillis(),
	})

	chatRuntime.mu.Lock()
	defer chatRuntime.mu.Unlock()
	if len(chatRuntime.requests) != 1 || len(chatRuntime.requests[0].Images) != 0 || len(chatRuntime.requests[0].LocalImages) != 1 {
		t.Fatalf("Codex image requests = %#v", chatRuntime.requests)
	}
	localImage := chatRuntime.requests[0].LocalImages[0]
	if localImage.MediaType != "image/png" || !filepath.IsAbs(localImage.Path) {
		t.Fatalf("Codex local image = %#v", localImage)
	}
	relative, err := filepath.Rel(store.MediaRoot(), localImage.Path)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("Codex local image escaped media root: root=%q path=%q", store.MediaRoot(), localImage.Path)
	}
	materialized, err := os.ReadFile(localImage.Path)
	if err != nil || string(materialized) != string(imageData) {
		t.Fatalf("materialized image = %v/%q, want %q", err, materialized, imageData)
	}
}
