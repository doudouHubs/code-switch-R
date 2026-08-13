package services

import (
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
