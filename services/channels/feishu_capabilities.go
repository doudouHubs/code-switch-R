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
	"net/url"
	"strconv"
	"strings"
)

func (p *feishuProvider) feishuEndpoint(path string) string {
	return strings.TrimRight(p.apiBaseURL, "/") + path
}

func (p *feishuProvider) uploadFeishuMedia(ctx context.Context, media ChannelMedia, resourceType, fileType string) (string, error) {
	if len(media.Data) == 0 {
		return "", errors.New("Feishu media is empty")
	}
	fileName := strings.TrimSpace(media.FileName)
	if fileName == "" {
		fileName = "upload.bin"
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if resourceType == "image" {
		if err := writer.WriteField("image_type", "message"); err != nil {
			return "", err
		}
	} else {
		if fileType == "" {
			fileType = "stream"
		}
		if err := writer.WriteField("file_type", fileType); err != nil {
			return "", err
		}
		if err := writer.WriteField("file_name", fileName); err != nil {
			return "", err
		}
	}
	field := "image"
	if resourceType == "file" {
		field = "file"
	}
	part, err := writer.CreateFormFile(field, fileName)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(media.Data); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	headers, err := p.auth(ctx)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.feishuEndpoint("/open-apis/im/v1/"+resourceType+"s"), &body)
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
	data, err := io.ReadAll(io.LimitReader(response.Body, channelMaxHTTPBody+1))
	if err != nil {
		return "", err
	}
	if len(data) > channelMaxHTTPBody {
		return "", errors.New("Feishu media response is too large")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("Feishu media upload failed: %s", response.Status)
	}
	var value struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			ImageKey string `json:"image_key"`
			FileKey  string `json:"file_key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return "", fmt.Errorf("decode Feishu media response: %w", err)
	}
	if value.Code != 0 {
		return "", fmt.Errorf("Feishu media upload rejected: %s", value.Msg)
	}
	key := value.Data.ImageKey
	if key == "" {
		key = value.Data.FileKey
	}
	if key == "" {
		return "", errors.New("Feishu media upload returned no key")
	}
	return key, nil
}

func (p *feishuProvider) sendFeishuMediaMessage(ctx context.Context, chatID, messageType string, content map[string]any) (string, error) {
	headers, err := p.auth(ctx)
	if err != nil {
		return "", err
	}
	var value struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	body := map[string]any{
		"receive_id": chatID,
		"msg_type":   messageType,
		"content":    mustJSON(content),
	}
	if err := doJSON(ctx, p.client, http.MethodPost, p.feishuEndpoint("/open-apis/im/v1/messages?receive_id_type=chat_id"), headers, body, &value); err != nil {
		return "", err
	}
	if value.Code != 0 {
		return "", fmt.Errorf("Feishu message rejected: %s", value.Msg)
	}
	return value.Data.MessageID, nil
}

func (p *feishuProvider) SendFeishuImage(ctx context.Context, chatID string, media ChannelMedia) (string, error) {
	if p.wsURL != "" {
		return p.relayMedia(ctx, chatID, media, "")
	}
	key, err := p.uploadFeishuMedia(ctx, media, "image", "")
	if err != nil {
		return "", err
	}
	return p.sendFeishuMediaMessage(ctx, chatID, "image", map[string]any{"image_key": key})
}

func normalizeFeishuFileType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "opus", "mp4", "pdf", "doc", "xls", "ppt", "stream":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "stream"
	}
}

func (p *feishuProvider) SendFeishuFile(ctx context.Context, chatID string, media ChannelMedia, fileType string) (string, error) {
	if p.wsURL != "" {
		return p.relayMedia(ctx, chatID, media, "")
	}
	key, err := p.uploadFeishuMedia(ctx, media, "file", normalizeFeishuFileType(fileType))
	if err != nil {
		return "", err
	}
	return p.sendFeishuMediaMessage(ctx, chatID, "file", map[string]any{"file_key": key})
}

func (p *feishuProvider) ListFeishuChatMembers(ctx context.Context, chatID string, pageSize int, pageToken, memberIDType string) (FeishuChatMemberPage, error) {
	if pageSize <= 0 || pageSize > 50 {
		pageSize = 50
	}
	switch memberIDType {
	case "user_id", "union_id":
	default:
		memberIDType = "open_id"
	}
	query := url.Values{}
	query.Set("member_id_type", memberIDType)
	query.Set("page_size", strconv.Itoa(pageSize))
	if strings.TrimSpace(pageToken) != "" {
		query.Set("page_token", strings.TrimSpace(pageToken))
	}
	headers, err := p.auth(ctx)
	if err != nil {
		return FeishuChatMemberPage{}, err
	}
	var value struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data FeishuChatMemberPage `json:"data"`
	}
	endpoint := p.feishuEndpoint("/open-apis/im/v1/chats/" + url.PathEscape(strings.TrimSpace(chatID)) + "/members?" + query.Encode())
	if err := doJSON(ctx, p.client, http.MethodGet, endpoint, headers, nil, &value); err != nil {
		return FeishuChatMemberPage{}, err
	}
	if value.Code != 0 {
		return FeishuChatMemberPage{}, fmt.Errorf("Feishu listChatMembers rejected: %s", value.Msg)
	}
	return value.Data, nil
}

func (p *feishuProvider) feishuChatType(ctx context.Context, chatID string) (string, error) {
	headers, err := p.auth(ctx)
	if err != nil {
		return "", err
	}
	var value struct {
		Code int `json:"code"`
		Data struct {
			ChatType string `json:"chat_type"`
		} `json:"data"`
	}
	if err := doJSON(ctx, p.client, http.MethodGet, p.feishuEndpoint("/open-apis/im/v1/chats/"+url.PathEscape(strings.TrimSpace(chatID))), headers, nil, &value); err != nil {
		return "", err
	}
	if value.Code != 0 {
		return "", errors.New("Feishu chat lookup rejected")
	}
	return value.Data.ChatType, nil
}

func (p *feishuProvider) AtFeishuMember(ctx context.Context, chatID string, userIDs []string, atAll bool, text string) (string, error) {
	if chatType, err := p.feishuChatType(ctx, chatID); err != nil {
		return "", err
	} else if chatType != "group" {
		return "", errors.New("FeishuAtMember is only available in group chats")
	}
	elements := make([]map[string]any, 0, len(userIDs)+2)
	if atAll {
		elements = append(elements, map[string]any{"tag": "at", "user_id": "all"})
	}
	for _, userID := range userIDs {
		if value := strings.TrimSpace(userID); value != "" {
			elements = append(elements, map[string]any{"tag": "at", "user_id": value})
		}
	}
	if value := strings.TrimSpace(text); value != "" {
		if len(elements) > 0 {
			value = " " + value
		}
		elements = append(elements, map[string]any{"tag": "text", "text": value})
	}
	if len(elements) == 0 {
		return "", errors.New("Feishu mention message is empty")
	}
	content := map[string]any{"zh_cn": map[string]any{"content": [][]map[string]any{elements}}}
	return p.sendFeishuMediaMessage(ctx, chatID, "post", content)
}

func (p *feishuProvider) SendFeishuUrgent(ctx context.Context, messageID string, userIDs, urgentTypes []string) (bool, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" || len(userIDs) == 0 || len(urgentTypes) == 0 {
		return false, errors.New("message_id, user_ids and urgent_types are required")
	}
	validUsers := make([]string, 0, len(userIDs))
	for _, userID := range userIDs {
		if value := strings.TrimSpace(userID); value != "" {
			validUsers = append(validUsers, value)
		}
	}
	if len(validUsers) == 0 {
		return false, errors.New("user_ids cannot be empty")
	}
	headers, err := p.auth(ctx)
	if err != nil {
		return false, err
	}
	for _, urgentType := range urgentTypes {
		urgentType = strings.ToLower(strings.TrimSpace(urgentType))
		if urgentType != "app" && urgentType != "sms" {
			return false, fmt.Errorf("unsupported Feishu urgent type %q", urgentType)
		}
		var value struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		endpoint := p.feishuEndpoint("/open-apis/im/v1/messages/"+url.PathEscape(messageID)+"/urgent?user_id_type=user_id")
		body := map[string]any{"user_id_list": validUsers, "urgent_type": urgentType}
		if err := doJSON(ctx, p.client, http.MethodPost, endpoint, headers, body, &value); err != nil {
			return false, err
		}
		if value.Code != 0 {
			return false, fmt.Errorf("Feishu urgent rejected: %s", value.Msg)
		}
	}
	return true, nil
}

func (p *feishuProvider) feishuBitableRequest(ctx context.Context, method, path string, body any) (FeishuBitableData, error) {
	headers, err := p.auth(ctx)
	if err != nil {
		return nil, err
	}
	var value struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := doJSON(ctx, p.client, method, p.feishuEndpoint(path), headers, body, &value); err != nil {
		return nil, err
	}
	if value.Code != 0 {
		return nil, fmt.Errorf("Feishu Bitable request rejected: %s", value.Msg)
	}
	if len(value.Data) == 0 {
		value.Data = json.RawMessage(`{}`)
	}
	return FeishuBitableData(value.Data), nil
}

func bitableQuery(pageSize int, pageToken string) string {
	if pageSize <= 0 {
		pageSize = 50
	}
	query := url.Values{"page_size": []string{strconv.Itoa(pageSize)}}
	if strings.TrimSpace(pageToken) != "" {
		query.Set("page_token", strings.TrimSpace(pageToken))
	}
	return query.Encode()
}

func (p *feishuProvider) ListFeishuBitableApps(ctx context.Context, pageSize int, pageToken string) (FeishuBitableData, error) {
	return p.feishuBitableRequest(ctx, http.MethodGet, "/open-apis/bitable/v1/apps?"+bitableQuery(pageSize, pageToken), nil)
}

func (p *feishuProvider) ListFeishuBitableTables(ctx context.Context, appToken string, pageSize int, pageToken string) (FeishuBitableData, error) {
	return p.feishuBitableRequest(ctx, http.MethodGet, "/open-apis/bitable/v1/apps/"+url.PathEscape(strings.TrimSpace(appToken))+"/tables?"+bitableQuery(pageSize, pageToken), nil)
}

func (p *feishuProvider) ListFeishuBitableFields(ctx context.Context, appToken, tableID string, pageSize int, pageToken string) (FeishuBitableData, error) {
	path := "/open-apis/bitable/v1/apps/" + url.PathEscape(strings.TrimSpace(appToken)) + "/tables/" + url.PathEscape(strings.TrimSpace(tableID)) + "/fields?" + bitableQuery(pageSize, pageToken)
	return p.feishuBitableRequest(ctx, http.MethodGet, path, nil)
}

func (p *feishuProvider) GetFeishuBitableRecords(ctx context.Context, appToken, tableID string, pageSize int, pageToken, filter string) (FeishuBitableData, error) {
	query := bitableQuery(pageSize, pageToken)
	if strings.TrimSpace(filter) != "" {
		query += "&filter=" + url.QueryEscape(strings.TrimSpace(filter))
	}
	path := "/open-apis/bitable/v1/apps/" + url.PathEscape(strings.TrimSpace(appToken)) + "/tables/" + url.PathEscape(strings.TrimSpace(tableID)) + "/records?" + query
	return p.feishuBitableRequest(ctx, http.MethodGet, path, nil)
}

func (p *feishuProvider) CreateFeishuBitableRecords(ctx context.Context, appToken, tableID string, records []map[string]any) (FeishuBitableData, error) {
	path := "/open-apis/bitable/v1/apps/" + url.PathEscape(strings.TrimSpace(appToken)) + "/tables/" + url.PathEscape(strings.TrimSpace(tableID)) + "/records"
	return p.feishuBitableRequest(ctx, http.MethodPost, path, map[string]any{"records": records})
}

func (p *feishuProvider) UpdateFeishuBitableRecords(ctx context.Context, appToken, tableID string, records []map[string]any) (FeishuBitableData, error) {
	path := "/open-apis/bitable/v1/apps/" + url.PathEscape(strings.TrimSpace(appToken)) + "/tables/" + url.PathEscape(strings.TrimSpace(tableID)) + "/records"
	return p.feishuBitableRequest(ctx, http.MethodPut, path, map[string]any{"records": records})
}

func (p *feishuProvider) DeleteFeishuBitableRecords(ctx context.Context, appToken, tableID string, recordIDs []string) (FeishuBitableData, error) {
	path := "/open-apis/bitable/v1/apps/" + url.PathEscape(strings.TrimSpace(appToken)) + "/tables/" + url.PathEscape(strings.TrimSpace(tableID)) + "/records/batch_delete"
	return p.feishuBitableRequest(ctx, http.MethodPost, path, map[string]any{"record_ids": recordIDs})
}
