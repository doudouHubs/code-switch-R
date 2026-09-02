package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPetBrowserBridgeHealthTokenAndOriginBoundary(t *testing.T) {
	bridge := NewPetBrowserBridge(PetBrowserBridgeDependencies{})
	server := httptest.NewServer(bridge)
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL+petBrowserBridgeHealthPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "http://127.0.0.1:5173")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", response.StatusCode)
	}
	if got := response.Header.Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:5173" {
		t.Fatalf("health CORS origin = %q", got)
	}
	var health struct {
		OK    bool   `json:"ok"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if !health.OK || strings.TrimSpace(health.Token) == "" {
		t.Fatalf("health payload = %+v", health)
	}

	call := func(token, origin string) *http.Response {
		t.Helper()
		body := strings.NewReader(`{"method":"codeswitch/services.DoesNotExist","args":[]}`)
		req, err := http.NewRequest(http.MethodPost, server.URL+petBrowserBridgePath, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CodeSwitch-Pet-Token", token)
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	unauthorized := call("wrong-token", "http://127.0.0.1:5173")
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d", unauthorized.StatusCode)
	}

	unknown := call(health.Token, "http://127.0.0.1:5173")
	unknown.Body.Close()
	if unknown.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown method status = %d", unknown.StatusCode)
	}

	blocked := call(health.Token, "http://evil.example:5173")
	blocked.Body.Close()
	if blocked.StatusCode != http.StatusForbidden {
		t.Fatalf("blocked origin status = %d", blocked.StatusCode)
	}
}

func TestPetBrowserBridgePreflightAllowsTokenHeader(t *testing.T) {
	bridge := NewPetBrowserBridge(PetBrowserBridgeDependencies{})
	request := httptest.NewRequest(http.MethodOptions, petBrowserBridgePath, nil)
	request.Header.Set("Origin", "http://localhost:5173")
	request.Header.Set("Access-Control-Request-Headers", "content-type,x-codeswitch-pet-token")
	recorder := httptest.NewRecorder()

	bridge.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d", recorder.Code)
	}
	if !strings.Contains(strings.ToLower(recorder.Header().Get("Access-Control-Allow-Headers")), "x-codeswitch-pet-token") {
		t.Fatalf("preflight headers = %q", recorder.Header().Get("Access-Control-Allow-Headers"))
	}
}

func TestPetBrowserBridgeAllowsViteFallbackPorts(t *testing.T) {
	bridge := NewPetBrowserBridge(PetBrowserBridgeDependencies{})

	for _, port := range []string{"5173", "5174", "5199"} {
		request := httptest.NewRequest(http.MethodGet, petBrowserBridgeHealthPath, nil)
		request.Header.Set("Origin", "http://127.0.0.1:"+port)
		recorder := httptest.NewRecorder()

		bridge.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("Vite port %s status = %d", port, recorder.Code)
		}
		if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:"+port {
			t.Fatalf("Vite port %s CORS origin = %q", port, got)
		}
	}

	blocked := httptest.NewRequest(http.MethodGet, petBrowserBridgeHealthPath, nil)
	blocked.Header.Set("Origin", "http://127.0.0.1:5200")
	blockedRecorder := httptest.NewRecorder()
	bridge.ServeHTTP(blockedRecorder, blocked)
	if blockedRecorder.Code != http.StatusForbidden {
		t.Fatalf("blocked Vite port status = %d", blockedRecorder.Code)
	}
}

type petBrowserChannelFixture struct {
	calls          []string
	savedPayload   []byte
	lastID         string
	lastInstanceID string
	lastSessionID  string
	lastChatID     string
	lastContent    string
	lastLimit      int
	lastEnabled    bool
	lastSessionKey string
}

func (f *petBrowserChannelFixture) ListDescriptors() (interface{}, error) {
	f.calls = append(f.calls, "ListDescriptors")
	return map[string]string{"kind": "descriptor"}, nil
}

func (f *petBrowserChannelFixture) ListInstances() (interface{}, error) {
	f.calls = append(f.calls, "ListInstances")
	return map[string]string{"kind": "instance"}, nil
}

func (f *petBrowserChannelFixture) ListProjects() (interface{}, error) {
	f.calls = append(f.calls, "ListProjects")
	return map[string]string{"kind": "project"}, nil
}

func (f *petBrowserChannelFixture) SaveInstance(payload []byte) error {
	f.calls = append(f.calls, "SaveInstance")
	f.savedPayload = append([]byte(nil), payload...)
	return nil
}

func (f *petBrowserChannelFixture) RemoveInstance(id string) error {
	f.calls = append(f.calls, "RemoveInstance")
	f.lastID = id
	return nil
}

func (f *petBrowserChannelFixture) SetEnabled(id string, enabled bool) error {
	f.calls = append(f.calls, "SetEnabled")
	f.lastID = id
	f.lastEnabled = enabled
	return nil
}

func (f *petBrowserChannelFixture) Start(id string) error {
	f.calls = append(f.calls, "Start")
	f.lastID = id
	return nil
}

func (f *petBrowserChannelFixture) Stop(id string) error {
	f.calls = append(f.calls, "Stop")
	f.lastID = id
	return nil
}

func (f *petBrowserChannelFixture) GetStatus(id string) (interface{}, error) {
	f.calls = append(f.calls, "GetStatus")
	f.lastID = id
	return map[string]string{"state": "running"}, nil
}

func (f *petBrowserChannelFixture) ListSessions(instanceID string) (interface{}, error) {
	f.calls = append(f.calls, "ListSessions")
	f.lastInstanceID = instanceID
	return []string{"session-1"}, nil
}

func (f *petBrowserChannelFixture) ListMessages(sessionID string, limit int) (interface{}, error) {
	f.calls = append(f.calls, "ListMessages")
	f.lastSessionID = sessionID
	f.lastLimit = limit
	return []string{"message-1"}, nil
}

func (f *petBrowserChannelFixture) SendMessage(instanceID, chatID, content string) (string, error) {
	f.calls = append(f.calls, "SendMessage")
	f.lastInstanceID = instanceID
	f.lastChatID = chatID
	f.lastContent = content
	return "message-1", nil
}

func (f *petBrowserChannelFixture) StartWeixinLogin(instanceID string) (interface{}, error) {
	f.calls = append(f.calls, "StartWeixinLogin")
	f.lastInstanceID = instanceID
	return map[string]string{"status": "wait"}, nil
}

func (f *petBrowserChannelFixture) WaitWeixinLogin(instanceID, sessionKey string) (interface{}, error) {
	f.calls = append(f.calls, "WaitWeixinLogin")
	f.lastInstanceID = instanceID
	f.lastSessionKey = sessionKey
	return map[string]bool{"connected": false}, nil
}

func (f *petBrowserChannelFixture) CancelWeixinLogin(instanceID, sessionKey string) error {
	f.calls = append(f.calls, "CancelWeixinLogin")
	f.lastInstanceID = instanceID
	f.lastSessionKey = sessionKey
	return nil
}

func TestPetBrowserBridgeChannelWhitelistDelegatesAllPageMethods(t *testing.T) {
	fixture := &petBrowserChannelFixture{}
	bridge := NewPetBrowserBridge(PetBrowserBridgeDependencies{Channels: fixture})
	encodeArgs := func(values ...interface{}) []json.RawMessage {
		t.Helper()
		args := make([]json.RawMessage, 0, len(values))
		for _, value := range values {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			args = append(args, encoded)
		}
		return args
	}

	// 页面只需要这些方法；逐一走 dispatch，确保窄接口新增方法时不会出现“前端能打开但调用被拒绝”的断接。
	cases := []struct {
		name   string
		method string
		args   []interface{}
	}{
		{name: "descriptors", method: "codeswitch/services/channels.ChannelService.ListDescriptors"},
		{name: "instances", method: "codeswitch/services/channels.ChannelService.ListInstances"},
		{name: "projects", method: "codeswitch/services/channels.ChannelService.ListProjects"},
		{name: "save", method: "codeswitch/services/channels.ChannelService.SaveInstance", args: []interface{}{map[string]string{"id": "instance-1", "type": "feishu-bot"}}},
		{name: "remove", method: "codeswitch/services/channels.ChannelService.RemoveInstance", args: []interface{}{"instance-1"}},
		{name: "enable", method: "codeswitch/services/channels.ChannelService.SetEnabled", args: []interface{}{"instance-1", true}},
		{name: "start", method: "codeswitch/services/channels.ChannelService.Start", args: []interface{}{"instance-1"}},
		{name: "stop", method: "codeswitch/services/channels.ChannelService.Stop", args: []interface{}{"instance-1"}},
		{name: "status", method: "codeswitch/services/channels.ChannelService.GetStatus", args: []interface{}{"instance-1"}},
		{name: "sessions", method: "codeswitch/services/channels.ChannelService.ListSessions", args: []interface{}{"instance-1"}},
		{name: "messages", method: "codeswitch/services/channels.ChannelService.ListMessages", args: []interface{}{"session-1", 25}},
		{name: "send", method: "codeswitch/services/channels.ChannelService.SendMessage", args: []interface{}{"instance-1", "chat-1", "hello"}},
		{name: "weixin-start", method: "codeswitch/services/channels.ChannelService.StartWeixinLogin", args: []interface{}{"instance-1"}},
		{name: "weixin-wait", method: "codeswitch/services/channels.ChannelService.WaitWeixinLogin", args: []interface{}{"instance-1", "session-1"}},
		{name: "weixin-cancel", method: "codeswitch/services/channels.ChannelService.CancelWeixinLogin", args: []interface{}{"instance-1", "session-1"}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			value, err := bridge.dispatch(context.Background(), testCase.method, encodeArgs(testCase.args...))
			if err != nil {
				t.Fatalf("dispatch %s: %v", testCase.method, err)
			}
			if testCase.name == "send" && value != "message-1" {
				t.Fatalf("send result = %#v", value)
			}
		})
	}

	if len(fixture.calls) != len(cases) {
		t.Fatalf("delegated calls = %v", fixture.calls)
	}
	if string(fixture.savedPayload) != `{"id":"instance-1","type":"feishu-bot"}` {
		t.Fatalf("saved payload = %s", fixture.savedPayload)
	}
	if fixture.lastID != "instance-1" || !fixture.lastEnabled {
		t.Fatalf("enable/status args = id:%q enabled:%t", fixture.lastID, fixture.lastEnabled)
	}
	if fixture.lastInstanceID != "instance-1" || fixture.lastSessionID != "session-1" || fixture.lastLimit != 25 {
		t.Fatalf("history args = instance:%q session:%q limit:%d", fixture.lastInstanceID, fixture.lastSessionID, fixture.lastLimit)
	}
	if fixture.lastChatID != "chat-1" || fixture.lastContent != "hello" {
		t.Fatalf("send args = chat:%q content:%q", fixture.lastChatID, fixture.lastContent)
	}
	if _, err := bridge.dispatch(context.Background(), "codeswitch/services.ChannelService.ListInstances", nil); err == nil {
		t.Fatal("legacy channel service method name was unexpectedly accepted")
	}
}

type petBrowserChatHistoryRuntime struct {
	historyRequest PetChatHistoryRequest
	historyCalls   int
}

func (r *petBrowserChatHistoryRuntime) StartChat(_ context.Context, _ PetChatRequest) (PetChatStartResult, error) {
	return PetChatStartResult{}, nil
}

func (r *petBrowserChatHistoryRuntime) CancelChat(string) error { return nil }

func (r *petBrowserChatHistoryRuntime) Close() error { return nil }

func (r *petBrowserChatHistoryRuntime) GetChatHistory(_ context.Context, request PetChatHistoryRequest) (PetChatHistoryResult, error) {
	r.historyCalls++
	r.historyRequest = request
	return PetChatHistoryResult{
		ThreadID: "bridge-history-thread",
		Messages: []PetChatHistoryMessage{{Role: "assistant", Content: "bridge history"}},
	}, nil
}

func TestPetBrowserBridgeGetChatHistoryDelegatesToAIAPI(t *testing.T) {
	runtime := &petBrowserChatHistoryRuntime{}
	bridge := NewPetBrowserBridge(PetBrowserBridgeDependencies{
		AI: NewPetAIAPIServiceWithChatRuntime(nil, runtime),
	})
	request := PetChatHistoryRequest{PetID: "bridge-pet", Persona: "bridge persona"}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}

	value, err := bridge.dispatch(context.Background(), "codeswitch/services.PetAIAPIService.GetChatHistory", []json.RawMessage{encoded})
	if err != nil {
		t.Fatalf("GetChatHistory dispatch error = %v", err)
	}
	result, ok := value.(PetChatHistoryResult)
	if !ok {
		t.Fatalf("GetChatHistory result type = %T", value)
	}
	if result.ThreadID != "bridge-history-thread" || len(result.Messages) != 1 || result.Messages[0].Content != "bridge history" {
		t.Fatalf("GetChatHistory result = %#v", result)
	}
	if runtime.historyCalls != 1 || runtime.historyRequest != request {
		t.Fatalf("history runtime calls/request = %d / %#v", runtime.historyCalls, runtime.historyRequest)
	}
}
