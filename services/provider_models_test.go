package services

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchProviderModelsUsesProviderEndpointAndAuth(t *testing.T) {
	tests := []struct {
		name              string
		authType          string
		wantAuthHeader    string
		wantAuthValue     string
		wantAnthropicHead string
	}{
		{
			name:           "默认 bearer",
			wantAuthHeader: "Authorization",
			wantAuthValue:  "Bearer test-key",
		},
		{
			name:              "Anthropic x-api-key",
			authType:          "x-api-key",
			wantAuthHeader:    "x-api-key",
			wantAuthValue:     "test-key",
			wantAnthropicHead: "2023-06-01",
		},
		{
			name:           "自定义 header",
			authType:       "X-Custom-Key",
			wantAuthHeader: "X-Custom-Key",
			wantAuthValue:  "test-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/v1/models" {
					t.Errorf("models endpoint = %q, want /v1/models", request.URL.Path)
				}
				if got := request.Header.Get(tt.wantAuthHeader); got != tt.wantAuthValue {
					t.Errorf("%s = %q, want %q", tt.wantAuthHeader, got, tt.wantAuthValue)
				}
				if got := request.Header.Get("anthropic-version"); got != tt.wantAnthropicHead {
					t.Errorf("anthropic-version = %q, want %q", got, tt.wantAnthropicHead)
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"data":[{"id":"z-model","display_name":"Z model"},{"id":"a-model","name":"A model"},{"id":"z-model"}]}`))
			}))
			defer server.Close()

			models, err := fetchProviderModels(Provider{
				Name:                 "test-provider",
				APIURL:               server.URL,
				APIKey:               "test-key",
				ConnectivityAuthType: tt.authType,
			}, server.Client())
			if err != nil {
				t.Fatalf("fetchProviderModels() error = %v", err)
			}
			if len(models) != 2 {
				t.Fatalf("got %d models, want 2: %#v", len(models), models)
			}
			if models[0].ID != "a-model" || models[0].Name != "A model" {
				t.Errorf("first model = %#v, want a-model/A model", models[0])
			}
			if models[1].ID != "z-model" || models[1].Name != "Z model" {
				t.Errorf("second model = %#v, want z-model/Z model", models[1])
			}
		})
	}
}

func TestDecodeProviderModelsSupportsArrayAndStringEntries(t *testing.T) {
	models, err := decodeProviderModels([]byte(`["model-b", {"id":"model-a"}, {"slug":"model-c"}]`))
	if err != nil {
		t.Fatalf("decodeProviderModels() error = %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("got %d models, want 3: %#v", len(models), models)
	}
	for index, want := range []string{"model-a", "model-b", "model-c"} {
		if models[index].ID != want {
			t.Errorf("models[%d].ID = %q, want %q", index, models[index].ID, want)
		}
	}
}

func TestDecodeProviderModelsCarriesAndInfersImageCategory(t *testing.T) {
	models, err := decodeProviderModels([]byte(`{"data":[
		{"id":"explicit-image","category":"image_generation"},
		{"id":"flag-image","supportsImageGeneration":true},
		{"id":"gpt-image-1"},
		{"id":"dall-e-3"},
		{"id":"text-model","type":"model"}
	]}`))
	if err != nil {
		t.Fatalf("decodeProviderModels() error = %v", err)
	}

	want := map[string]string{
		"explicit-image": "image",
		"flag-image":     "image",
		"gpt-image-1":    "image",
		"dall-e-3":       "image",
		"text-model":     "",
	}
	if len(models) != len(want) {
		t.Fatalf("got %d models, want %d: %#v", len(models), len(want), models)
	}
	for _, model := range models {
		if got := model.ModelCategory; got != want[model.ID] {
			t.Errorf("model %q category = %q, want %q", model.ID, got, want[model.ID])
		}
	}
}
