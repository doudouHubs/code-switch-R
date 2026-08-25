package channels

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

type wecomProvider struct {
	*relayProvider
	corpID, secret, agentID, token string
	expires                        int64
	client                         *http.Client
}

func newWeComProvider(instance ChannelInstance, notify EventSink) (ChannelProvider, error) {
	corp := strings.TrimSpace(instance.Config["corpId"])
	secret := strings.TrimSpace(instance.Config["secret"])
	agent := strings.TrimSpace(instance.Config["agentId"])
	if corp == "" || secret == "" || agent == "" {
		return nil, errors.New("WeCom corpId, secret and agentId are required")
	}
	return &wecomProvider{relayProvider: newRelayProvider(instance, notify), corpID: corp, secret: secret, agentID: agent, client: channelHTTPClient()}, nil
}
func (p *wecomProvider) ensureToken(ctx context.Context) (string, error) {
	if p.token != "" && nowMillis() < p.expires {
		return p.token, nil
	}
	var value struct {
		ErrCode int    `json:"errcode"`
		Token   string `json:"access_token"`
		Expires int64  `json:"expires_in"`
	}
	endpoint := "https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=" + p.corpID + "&corpsecret=" + p.secret
	if err := doJSON(ctx, p.client, http.MethodGet, endpoint, nil, nil, &value); err != nil {
		return "", err
	}
	if value.ErrCode != 0 || value.Token == "" {
		return "", errors.New("WeCom authentication rejected")
	}
	p.token = value.Token
	expires := value.Expires
	if expires <= 60 {
		expires = 7200
	}
	p.expires = nowMillis() + (expires-60)*1000
	return p.token, nil
}
func (p *wecomProvider) Start(ctx context.Context) error {
	if _, err := p.ensureToken(ctx); err != nil {
		return err
	}
	return p.startRelay(ctx)
}
func (p *wecomProvider) Stop(ctx context.Context) error { return p.stopRelay(ctx) }
func (p *wecomProvider) IsRunning() bool                { return p.isRunning() }
func (p *wecomProvider) SendMessage(ctx context.Context, chatID, content string) (string, error) {
	if p.wsURL != "" {
		return p.relaySend(ctx, "send_message", chatID, content, ChannelMedia{})
	}
	token, err := p.ensureToken(ctx)
	if err != nil {
		return "", err
	}
	var value struct {
		ErrCode int    `json:"errcode"`
		MsgID   string `json:"msgid"`
	}
	body := map[string]any{"touser": chatID, "msgtype": "text", "agentid": atoi(p.agentID), "text": map[string]string{"content": content}}
	if err := doJSON(ctx, p.client, http.MethodPost, "https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token="+token, nil, body, &value); err != nil {
		return "", err
	}
	if value.ErrCode != 0 {
		return "", errors.New("WeCom rejected sendMessage")
	}
	return value.MsgID, nil
}
func (p *wecomProvider) ReplyMessage(ctx context.Context, messageID, content string) (string, error) {
	chatID := p.relayReplyChat(messageID)
	if chatID == "" {
		return "", errors.New("WeCom reply context not found")
	}
	return p.SendMessage(ctx, chatID, content)
}
func (p *wecomProvider) GetGroupMessages(context.Context, string, int) ([]ChannelMessage, error) {
	return []ChannelMessage{}, nil
}
func (p *wecomProvider) ListGroups(context.Context) ([]ChannelGroup, error) {
	return []ChannelGroup{}, nil
}
func (p *wecomProvider) SendMedia(ctx context.Context, chatID string, media ChannelMedia, caption string) (string, error) {
	if p.wsURL != "" {
		return p.relayMedia(ctx, chatID, media, caption)
	}
	return "", errors.New("WeCom media delivery requires a configured relay")
}
func (p *wecomProvider) SupportsStreaming() bool { return false }
func (p *wecomProvider) SendStreamingMessage(context.Context, string, string, string) (StreamingHandle, error) {
	return nil, errors.New("WeCom streaming is not supported")
}
func atoi(value string) int { result, _ := strconv.Atoi(value); return result }
