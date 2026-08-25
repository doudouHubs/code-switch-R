package channels

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func parseIncoming(channelType string, raw []byte) (ChannelMessage, bool) {
	value := decodeJSONObject(raw)
	if value == nil {
		return ChannelMessage{}, false
	}
	message := ChannelMessage{Role: "user", Timestamp: nowMillis(), Raw: string(raw)}
	if nested, ok := value["message"].(map[string]any); ok && channelType == ChannelTypeTelegram {
		value = nested
	}
	switch channelType {
	case ChannelTypeDiscord:
		if event, _ := value["t"].(string); event == "MESSAGE_CREATE" {
			if nested, ok := value["d"].(map[string]any); ok {
				value = nested
			}
		}
	case ChannelTypeQQ:
		if event, _ := value["t"].(string); event != "C2C_MESSAGE_CREATE" && event != "GROUP_AT_MESSAGE_CREATE" && event != "AT_MESSAGE_CREATE" && event != "DIRECT_MESSAGE_CREATE" {
			return ChannelMessage{}, false
		}
		if nested, ok := value["d"].(map[string]any); ok {
			value = nested
		}
	case ChannelTypeDingTalk:
		if headers, ok := value["headers"].(map[string]any); ok {
			if topic, _ := headers["topic"].(string); topic != "" && topic != "/v1.0/im/bot/messages/get" {
				return ChannelMessage{}, false
			}
			if data, ok := value["data"].(string); ok {
				var nested map[string]any
				if json.Unmarshal([]byte(data), &nested) == nil {
					value = nested
				}
			}
		}
	case ChannelTypeFeishu:
		if header, ok := value["header"].(map[string]any); ok {
			if event, _ := header["event_type"].(string); event != "" && event != "im.message.receive_v1" {
				return ChannelMessage{}, false
			}
			if nested, ok := value["event"].(map[string]any); ok {
				value = nested
			}
			if nested, ok := value["message"].(map[string]any); ok {
				value = nested
			}
		}
	case ChannelTypeWeCom:
		if msgType, _ := value["MsgType"].(string); msgType != "" && msgType != "text" && msgType != "image" && msgType != "voice" && msgType != "file" {
			return ChannelMessage{}, false
		}
	}
	if envelopeChat, ok := value["chatId"].(string); ok && envelopeChat != "" {
		message.ChatID = envelopeChat
	} else {
		message.ChatID = firstString(value, "chat_id", "conversationId", "conversation_id", "channel_id", "FromUserName", "from_user_id")
		if channelType == ChannelTypeQQ {
			event, _ := value["event"].(string)
			_ = event
			if id := firstString(value, "group_openid", "group_id"); id != "" {
				message.ChatID = "group:" + id
			} else if id := firstString(value, "channel_id"); id != "" {
				message.ChatID = "channel:" + id
			} else if id := firstString(value, "user_openid", "openid"); id != "" {
				message.ChatID = "c2c:" + id
			}
		}
	}
	message.SenderID = firstString(value, "senderId", "sender_id", "senderStaffId", "FromUserName", "from_user_id")
	message.SenderName = firstString(value, "senderName", "sender_name", "senderNick", "username", "NickName")
	message.ExternalID = firstString(value, "messageId", "message_id", "msgId", "MsgId", "id", "client_id")
	message.Content = firstString(value, "content", "text", "Content")
	if nested, ok := value["text"].(map[string]any); ok {
		message.Content = firstString(nested, "content", "text")
	}
	if nested, ok := value["sender"].(map[string]any); ok {
		if id := firstString(nested, "sender_id", "id"); id != "" {
			message.SenderID = id
		}
	}
	if timestamp := firstNumber(value, "timestamp", "create_time", "createAt", "msgCreateTime", "CreateTime", "date", "create_time_ms"); timestamp > 0 {
		if timestamp < 1_000_000_000_000 {
			timestamp *= 1000
		}
		message.Timestamp = timestamp
	}
	message.Raw = string(raw)
	if message.ChatID == "" || message.ExternalID == "" || message.Content == "" {
		if attachments, ok := value["attachments"].([]any); ok && len(attachments) > 0 {
			message.Content = "[User sent an attachment]"
		}
	}
	if message.ChatID == "" || message.ExternalID == "" || message.Content == "" {
		return ChannelMessage{}, false
	}
	if message.SenderName == "" {
		message.SenderName = message.SenderID
	}
	if message.Timestamp < time.Now().Add(-15*time.Minute).UnixMilli() {
		return ChannelMessage{}, false
	}
	if message.Timestamp > time.Now().Add(5*time.Minute).UnixMilli() {
		message.Timestamp = nowMillis()
	}
	return message, true
}

func firstString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
		if number, ok := value[key].(float64); ok {
			return strconv.FormatInt(int64(number), 10)
		}
	}
	return ""
}

func firstNumber(value map[string]any, keys ...string) int64 {
	for _, key := range keys {
		switch typed := value[key].(type) {
		case float64:
			return int64(typed)
		case json.Number:
			if result, err := typed.Int64(); err == nil {
				return result
			}
		case string:
			if result, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64); err == nil {
				return result
			}
		}
	}
	return 0
}

func normalizeChatID(value string) string { return strings.TrimSpace(value) }

func relayPayload(action, chatID, content string, media ChannelMedia) map[string]any {
	payload := map[string]any{"action": action, "chatId": normalizeChatID(chatID), "content": content}
	if media.Kind != "" {
		payload["media"] = map[string]any{"kind": media.Kind, "mediaType": media.MediaType, "fileName": media.FileName, "data": media.Data}
	}
	return payload
}

func parseJSONError(value map[string]any) error {
	if value == nil {
		return nil
	}
	if code, ok := value["code"].(float64); ok && code != 0 {
		return fmt.Errorf("channel provider returned code %d", int(code))
	}
	if ok, exists := value["ok"].(bool); exists && !ok {
		return fmt.Errorf("channel provider rejected request")
	}
	if errValue, ok := value["error"].(map[string]any); ok {
		if message, _ := errValue["message"].(string); message != "" {
			return fmt.Errorf("channel provider rejected request: %s", message)
		}
	}
	return nil
}
