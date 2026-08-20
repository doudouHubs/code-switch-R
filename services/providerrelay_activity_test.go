package services

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestProviderRelayPayloadHasOutput(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{name: "openai chat", payload: `{"choices":[{"message":{"content":"hello"}}]}`, want: true},
		{name: "anthropic message", payload: `{"content":[{"type":"text","text":"hello"}]}`, want: true},
		{name: "responses delta", payload: `{"type":"response.output_text.delta","delta":"hello"}`, want: true},
		{name: "gemini parts", payload: `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`, want: true},
		{name: "usage only", payload: `{"usage":{"input_tokens":12,"output_tokens":0}}`, want: false},
		{name: "empty delta", payload: `{"choices":[{"delta":{"content":""}}]}`, want: false},
		{name: "error only", payload: `{"error":{"message":"upstream failed"}}`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := providerRelayPayloadHasOutput([]byte(tt.payload)); got != tt.want {
				t.Fatalf("providerRelayPayloadHasOutput() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProviderRelayActivityFinishesAtHandlerBoundary(t *testing.T) {
	var events []PetActivityEvent
	prs := &ProviderRelayService{
		activityEmitter: PetActivityEmitterFunc(func(event PetActivityEvent) error {
			events = append(events, event)
			return nil
		}),
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	prs.beginProviderRelayActivity(c)
	if len(events) != 0 {
		t.Fatalf("begin must not finish the request: %+v", events)
	}
	activity := providerRelayActivityFromContext(c)
	if activity == nil {
		t.Fatal("begin must attach an activity owner to the request context")
	}
	activity.Output()
	if len(events) != 1 || events[0].Phase != PetActivityOutput {
		t.Fatalf("unexpected output event: %+v", events)
	}

	prs.finishProviderRelayActivity(c)
	prs.finishProviderRelayActivity(c)
	if len(events) != 2 || events[1].Phase != PetActivityCompleted {
		t.Fatalf("expected one terminal event after handler finish: %+v", events)
	}
}

func TestProviderRelayResponseHooksPreserveNonStreamBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	requestLog := &ReqeustLog{}
	converter := NewOpenAIToAnthropicSSEConverter("claude-test")
	body := []byte(`{"choices":[{"message":{"content":"hello"}}]}`)

	hooks := providerRelayResponseHooks(c, "claude", requestLog, false, converter)
	if len(hooks) != 1 {
		t.Fatalf("non-stream response should only use request log hook, got %d hooks", len(hooks))
	}
	flush, got := hooks[0](body)
	if !flush || !reflect.DeepEqual(got, body) {
		t.Fatalf("non-stream hook changed response: flush=%v body=%s", flush, got)
	}
}
