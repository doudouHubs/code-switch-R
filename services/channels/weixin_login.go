package channels

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	defaultWeixinBaseURL   = "https://ilinkai.weixin.qq.com"
	weixinBotType          = "3"
	weixinQRCodePollWindow = 35 * time.Second
	weixinLoginTTL         = 5 * time.Minute
	weixinMaxQRRefreshes   = 3
)

type weixinLoginSession struct {
	instanceID string
	sessionKey string
	ctx        context.Context
	baseURL    string
	routeTag   string
	qrCode     string
	qrDataURL  string
	startedAt  time.Time
	refreshes  int
	cancel     context.CancelFunc
}

type WeixinLoginStartResult struct {
	SessionKey string `json:"sessionKey"`
	QRCode     string `json:"qrcode,omitempty"`
	QRDataURL  string `json:"qrDataUrl,omitempty"`
	QRURL      string `json:"qrUrl,omitempty"`
	Status     string `json:"status"`
	Message    string `json:"message"`
}

type WeixinLoginWaitResult struct {
	SessionKey string `json:"sessionKey,omitempty"`
	Status     string `json:"status"`
	Connected  bool   `json:"connected"`
	Message    string `json:"message"`
	QRCode     string `json:"qrcode,omitempty"`
	QRDataURL  string `json:"qrDataUrl,omitempty"`
	QRURL      string `json:"qrUrl,omitempty"`
	Token      string `json:"token,omitempty"`
	AccountID  string `json:"accountId,omitempty"`
	BaseURL    string `json:"baseUrl,omitempty"`
	UserID     string `json:"userId,omitempty"`
}

type weixinQRCodeResponse struct {
	QRCode    string `json:"qrcode"`
	QRDataURL string `json:"qrcode_img_content"`
}

type weixinQRCodeStatusResponse struct {
	Status    string `json:"status"`
	Token     string `json:"bot_token"`
	AccountID string `json:"ilink_bot_id"`
	BaseURL   string `json:"baseurl"`
	UserID    string `json:"ilink_user_id"`
}

func normalizeWeixinBaseURL(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" {
		return defaultWeixinBaseURL
	}
	return value
}

func (s *ChannelService) getWeixinLoginInstance(instanceID string) (ChannelInstance, error) {
	if s == nil || s.store == nil {
		return ChannelInstance{}, errors.New("channel service is unavailable")
	}
	instance, found, err := s.store.GetInstance(strings.TrimSpace(instanceID))
	if err != nil {
		return ChannelInstance{}, err
	}
	if !found {
		return ChannelInstance{}, errors.New("channel instance not found")
	}
	if instance.Type != ChannelTypeWeixin {
		return ChannelInstance{}, errors.New("Weixin QR login requires a WeChat Official channel")
	}
	return instance, nil
}

func weixinLoginHeaders(routeTag string) map[string]string {
	headers := map[string]string{}
	if strings.TrimSpace(routeTag) != "" {
		headers["SKRouteTag"] = strings.TrimSpace(routeTag)
	}
	return headers
}

func fetchWeixinQRCode(ctx context.Context, client *http.Client, baseURL, routeTag string) (weixinQRCodeResponse, error) {
	endpoint := normalizeWeixinBaseURL(baseURL) + "/ilink/bot/get_bot_qrcode?bot_type=" + url.QueryEscape(weixinBotType)
	var response weixinQRCodeResponse
	if err := doJSON(ctx, client, http.MethodGet, endpoint, weixinLoginHeaders(routeTag), nil, &response); err != nil {
		return weixinQRCodeResponse{}, err
	}
	if strings.TrimSpace(response.QRCode) == "" || strings.TrimSpace(response.QRDataURL) == "" {
		return weixinQRCodeResponse{}, errors.New("Weixin QR response is incomplete")
	}
	return response, nil
}

func (s *ChannelService) StartWeixinLogin(instanceID string) (WeixinLoginStartResult, error) {
	instance, err := s.getWeixinLoginInstance(instanceID)
	if err != nil {
		return WeixinLoginStartResult{}, err
	}
	baseURL := normalizeWeixinBaseURL(instance.Config["baseUrl"])
	routeTag := strings.TrimSpace(instance.Config["routeTag"])
	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	client := &http.Client{Timeout: 20 * time.Second}
	qr, err := fetchWeixinQRCode(sessionCtx, client, baseURL, routeTag)
	if err != nil {
		sessionCancel()
		return WeixinLoginStartResult{}, fmt.Errorf("start Weixin QR login: %w", err)
	}
	session := &weixinLoginSession{
		instanceID: instance.ID,
		sessionKey: uuid.NewString(),
		ctx:        sessionCtx,
		baseURL:    baseURL,
		routeTag:   routeTag,
		qrCode:     qr.QRCode,
		qrDataURL:  qr.QRDataURL,
		startedAt:  time.Now(),
		cancel:     sessionCancel,
	}
	s.weixinLoginMu.Lock()
	if previous := s.weixinLogins[instance.ID]; previous != nil && previous.cancel != nil {
		previous.cancel()
	}
	s.weixinLogins[instance.ID] = session
	s.weixinLoginMu.Unlock()
	return WeixinLoginStartResult{
		SessionKey: session.sessionKey,
		QRCode:     session.qrCode,
		QRDataURL:  session.qrDataURL,
		QRURL:      session.qrDataURL,
		Status:     "wait",
		Message:    "Scan the QR code below with WeChat to complete the connection.",
	}, nil
}

func (s *ChannelService) getWeixinLoginSession(instanceID, sessionKey string) (*weixinLoginSession, error) {
	instanceID = strings.TrimSpace(instanceID)
	s.weixinLoginMu.Lock()
	defer s.weixinLoginMu.Unlock()
	session := s.weixinLogins[strings.TrimSpace(instanceID)]
	if session == nil || session.sessionKey != strings.TrimSpace(sessionKey) {
		return nil, errors.New("Weixin login session is missing or expired")
	}
	if time.Since(session.startedAt) >= weixinLoginTTL {
		delete(s.weixinLogins, instanceID)
		if session.cancel != nil {
			session.cancel()
		}
		return nil, errors.New("Weixin QR code expired, please regenerate it")
	}
	return session, nil
}

func (s *ChannelService) WaitWeixinLogin(instanceID, sessionKey string) (WeixinLoginWaitResult, error) {
	instance, err := s.getWeixinLoginInstance(instanceID)
	if err != nil {
		return WeixinLoginWaitResult{}, err
	}
	session, err := s.getWeixinLoginSession(instance.ID, sessionKey)
	if err != nil {
		return WeixinLoginWaitResult{}, err
	}
	// session.ctx 是登录流程的唯一取消 owner；Wait 只追加单次轮询超时，
	// 这样刷新二维码、重新绑定或确认成功时，正在等待的 HTTP 请求都会立即退出。
	s.weixinLoginMu.Lock()
	requestBase := session.ctx
	baseURL := session.baseURL
	routeTag := session.routeTag
	qrCode := session.qrCode
	s.weixinLoginMu.Unlock()
	if requestBase == nil {
		requestBase = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(requestBase, weixinQRCodePollWindow)
	defer cancel()
	client := &http.Client{Timeout: weixinQRCodePollWindow + 5*time.Second}
	endpoint := normalizeWeixinBaseURL(baseURL) + "/ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(qrCode)
	var status weixinQRCodeStatusResponse
	headers := weixinLoginHeaders(routeTag)
	headers["iLink-App-ClientVersion"] = "1"
	if pollErr := doJSON(requestCtx, client, http.MethodGet, endpoint, headers, nil, &status); pollErr != nil {
		if errors.Is(pollErr, context.DeadlineExceeded) || errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return s.weixinWaitSnapshot(session, "wait", "Waiting for WeChat scan confirmation."), nil
		}
		if errors.Is(pollErr, context.Canceled) {
			return WeixinLoginWaitResult{}, errors.New("Weixin login was canceled")
		}
		return WeixinLoginWaitResult{}, fmt.Errorf("poll Weixin QR status: %w", pollErr)
	}

	switch strings.ToLower(strings.TrimSpace(status.Status)) {
	case "", "wait":
		return s.weixinWaitSnapshot(session, "wait", "Waiting for WeChat scan confirmation."), nil
	case "scaned":
		return s.weixinWaitSnapshot(session, "scaned", "QR code scanned. Confirm the login in WeChat."), nil
	case "expired":
		return s.refreshWeixinLoginQR(session)
	case "confirmed":
		if strings.TrimSpace(status.Token) == "" || strings.TrimSpace(status.AccountID) == "" {
			return WeixinLoginWaitResult{}, errors.New("Weixin login confirmation did not return credentials")
		}
		result := WeixinLoginWaitResult{SessionKey: session.sessionKey, Status: "confirmed", Connected: true, Message: "Connected to WeChat successfully.", Token: status.Token, AccountID: status.AccountID, BaseURL: normalizeWeixinBaseURL(status.BaseURL), UserID: status.UserID}
		s.CancelWeixinLogin(instance.ID, session.sessionKey)
		return result, nil
	default:
		return WeixinLoginWaitResult{}, fmt.Errorf("unsupported Weixin QR status %q", status.Status)
	}
}

func (s *ChannelService) weixinWaitSnapshot(session *weixinLoginSession, status, message string) WeixinLoginWaitResult {
	s.weixinLoginMu.Lock()
	defer s.weixinLoginMu.Unlock()
	return WeixinLoginWaitResult{SessionKey: session.sessionKey, Status: status, Message: message, QRCode: session.qrCode, QRDataURL: session.qrDataURL, QRURL: session.qrDataURL}
}

func (s *ChannelService) refreshWeixinLoginQR(session *weixinLoginSession) (WeixinLoginWaitResult, error) {
	s.weixinLoginMu.Lock()
	if current := s.weixinLogins[session.instanceID]; current != session {
		s.weixinLoginMu.Unlock()
		return WeixinLoginWaitResult{}, errors.New("Weixin login was canceled")
	}
	if session.refreshes >= weixinMaxQRRefreshes {
		delete(s.weixinLogins, session.instanceID)
		if session.cancel != nil {
			session.cancel()
		}
		s.weixinLoginMu.Unlock()
		return WeixinLoginWaitResult{SessionKey: session.sessionKey, Status: "expired", Message: "QR code expired multiple times. Please restart the login flow."}, nil
	}
	session.refreshes++
	requestCtx := session.ctx
	baseURL := session.baseURL
	routeTag := session.routeTag
	s.weixinLoginMu.Unlock()
	if requestCtx == nil {
		requestCtx = context.Background()
	}
	qr, err := fetchWeixinQRCode(requestCtx, &http.Client{Timeout: 20 * time.Second}, baseURL, routeTag)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return WeixinLoginWaitResult{}, errors.New("Weixin login was canceled")
		}
		return WeixinLoginWaitResult{}, err
	}
	s.weixinLoginMu.Lock()
	if current := s.weixinLogins[session.instanceID]; current != session {
		s.weixinLoginMu.Unlock()
		return WeixinLoginWaitResult{}, errors.New("Weixin login was canceled")
	}
	session.qrCode = qr.QRCode
	session.qrDataURL = qr.QRDataURL
	session.startedAt = time.Now()
	s.weixinLoginMu.Unlock()
	return s.weixinWaitSnapshot(session, "expired", "QR code expired. A new QR code is ready."), nil
}

func (s *ChannelService) CancelWeixinLogin(instanceID, sessionKey string) error {
	s.weixinLoginMu.Lock()
	defer s.weixinLoginMu.Unlock()
	instanceID = strings.TrimSpace(instanceID)
	session := s.weixinLogins[instanceID]
	if session == nil {
		return nil
	}
	if strings.TrimSpace(sessionKey) != "" && session.sessionKey != strings.TrimSpace(sessionKey) {
		return errors.New("Weixin login session does not match")
	}
	delete(s.weixinLogins, instanceID)
	if session.cancel != nil {
		session.cancel()
	}
	return nil
}
