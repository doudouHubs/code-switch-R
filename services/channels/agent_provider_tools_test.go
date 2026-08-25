package channels

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeswitch/services"
)

func TestChannelProviderToolDefinitionsStayPlatformScoped(t *testing.T) {
	feishu := channelProviderToolDefinitions(ChannelInstance{Type: ChannelTypeFeishu})
	weixin := channelProviderToolDefinitions(ChannelInstance{Type: ChannelTypeWeixin})
	other := channelProviderToolDefinitions(ChannelInstance{Type: ChannelTypeDiscord})

	if len(feishu) == 0 || len(weixin) == 0 || len(other) != 0 {
		t.Fatalf("provider tool definition counts = feishu:%d weixin:%d other:%d", len(feishu), len(weixin), len(other))
	}
	for _, definition := range feishu {
		if definition.Name == channelToolWeixinSendImage || definition.Name == channelToolWeixinSendFile {
			t.Fatalf("Feishu definitions exposed Weixin tool %q", definition.Name)
		}
	}
	for _, definition := range weixin {
		if definition.Name == channelToolFeishuSendImage || definition.Name == channelToolFeishuBitableListApps {
			t.Fatalf("Weixin definitions exposed Feishu tool %q", definition.Name)
		}
	}
}

func TestChannelProviderToolRejectsWrongPlatformBeforeManager(t *testing.T) {
	executor := &channelAgentToolExecutor{}
	result, err := executor.executeProviderTool(
		context.Background(),
		services.PetAgentToolCall{ID: "wrong-platform", Name: channelToolFeishuSendImage},
		map[string]json.RawMessage{
			"plugin_id": []byte(`"weixin"`),
			"chat_id":   []byte(`"chat"`),
			"file_path": []byte(`"missing.png"`),
		},
		services.PetAgentToolResult{ToolCallID: "wrong-platform", ToolName: string(channelToolFeishuSendImage)},
		ChannelInstance{ID: "weixin", Type: ChannelTypeWeixin},
	)
	if err != nil || !result.IsError || !strings.Contains(result.Content, ChannelTypeFeishu) {
		t.Fatalf("wrong-platform result = %#v, err=%v", result, err)
	}
}

func TestChannelMediaReadEnforcesWorkspaceAndSizeLimits(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "photo.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := &channelAgentToolExecutor{workspaceRoot: workspace, limits: services.DefaultPetAgentToolLimits()}
	media, err := executor.readChannelMedia(context.Background(), path, "image.png")
	if err != nil {
		t.Fatalf("read workspace media: %v", err)
	}
	if media.FileName != "photo.png" || media.MediaType != "image/png" || media.Kind != "" {
		t.Fatalf("workspace media = %#v", media)
	}
	converted := media.channelMedia()
	if converted.FileName != media.FileName || string(converted.Data) != string(media.Data) {
		t.Fatalf("converted media = %#v", converted)
	}

	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.readChannelMedia(context.Background(), outside, "image.png"); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("outside media error = %v", err)
	}

	executor.limits.MaxFileBytes = 4
	if _, err := executor.readChannelMedia(context.Background(), path, "image.png"); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized media error = %v", err)
	}
}

func TestChannelRemoteMediaReadUsesHTTPAndBounds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "image/png; charset=binary")
		_, _ = writer.Write([]byte("\x89PNG\r\n\x1a\nremote"))
	}))
	defer server.Close()

	media, err := readChannelRemoteMedia(context.Background(), mustParseProviderToolURL(t, server.URL+"/remote.png"), 1<<20, "image.png")
	if err != nil {
		t.Fatalf("read remote media: %v", err)
	}
	if media.FileName != "remote.png" || media.MediaType != "image/png" {
		t.Fatalf("remote media = %#v", media)
	}
}

func TestProviderToolRecordsConvertsJSONObjects(t *testing.T) {
	records, err := providerToolRecords(map[string]json.RawMessage{
		"records": []byte(`[{"fields":{"Name":"Ada"}},{"fields":{"Count":2}}]`),
	}, "records")
	if err != nil || len(records) != 2 || records[0]["fields"] == nil {
		t.Fatalf("records = %#v, err=%v", records, err)
	}
	if _, err := providerToolRecords(map[string]json.RawMessage{"records": []byte(`{"fields":{}}`)}, "records"); err == nil {
		t.Fatal("object payload should not be accepted as records array")
	}
}

func TestFeishuFileToolValidatesFileType(t *testing.T) {
	executor := &channelAgentToolExecutor{}
	result, err := executor.executeProviderTool(
		context.Background(),
		services.PetAgentToolCall{ID: "invalid-file-type", Name: channelToolFeishuSendFile},
		map[string]json.RawMessage{
			"plugin_id": []byte(`"feishu"`),
			"chat_id":   []byte(`"chat"`),
			"file_path": []byte(`"missing.bin"`),
			"file_type": []byte(`"exe"`),
		},
		services.PetAgentToolResult{ToolCallID: "invalid-file-type", ToolName: string(channelToolFeishuSendFile)},
		ChannelInstance{ID: "feishu", Type: ChannelTypeFeishu},
	)
	if err != nil || !result.IsError || !strings.Contains(result.Content, "file_type") {
		t.Fatalf("invalid-file-type result = %#v, err=%v", result, err)
	}
}

func mustParseProviderToolURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
