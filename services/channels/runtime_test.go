package channels

import (
	"context"
	"errors"
	"strings"
	"testing"

	"codeswitch/services"
)

func TestAgentRuntimeProviderResolverUsesClientDefaultReferenceAndIgnoresHistory(t *testing.T) {
	projectID := "project-a"
	providerID := "legacy-channel-provider"
	model := "legacy-channel-model"
	runtime := NewAgentRuntime(nil, nil, nil, nil, nil, nil, func(_ context.Context, _ ChannelInstance) (services.PetProviderReference, error) {
		return services.PetProviderReference{Platform: "Codex", ProviderID: "client-default", Model: "gpt-5.6-luna"}, nil
	})

	resolved, err := runtime.resolveProviderReference(context.Background(), ChannelInstance{
		ProjectID:        &projectID,
		ProviderPlatform: "claude",
		ProviderID:       &providerID,
		Model:            &model,
		Config:           map[string]string{"providerPlatform": "gemini"},
	})
	if err != nil {
		t.Fatalf("resolve default channel provider: %v", err)
	}
	if resolved.Platform != "codex" || resolved.ProviderID != "client-default" || resolved.Model != "gpt-5.6-luna" || resolved.Capability != services.PetCapabilityChat {
		t.Fatalf("resolved default provider = %#v", resolved)
	}
}

func TestAgentRuntimeProviderResolverRejectsIncompleteClientDefaultReference(t *testing.T) {
	runtime := NewAgentRuntime(nil, nil, nil, nil, nil, nil, func(context.Context, ChannelInstance) (services.PetProviderReference, error) {
		return services.PetProviderReference{Platform: "codex", ProviderID: "client-default"}, nil
	})
	_, err := runtime.resolveProviderReference(context.Background(), ChannelInstance{})
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete client default provider error = %v", err)
	}
}

func TestAgentRuntimeProviderResolverHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runtime := NewAgentRuntime(nil, nil, nil, nil, nil, nil, func(ctx context.Context, _ ChannelInstance) (services.PetProviderReference, error) {
		return services.PetProviderReference{}, ctx.Err()
	})
	_, err := runtime.resolveProviderReference(ctx, ChannelInstance{})
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("canceled provider resolution error = %v", err)
	}
}

func TestAgentRuntimeSendsFailureWhenDefaultProviderCannotBeResolved(t *testing.T) {
	workspace := t.TempDir()
	projectID := workspace
	instance := ChannelInstance{
		ID:          "channel-failure",
		Type:        "test-channel",
		Name:        "Failure Channel",
		Enabled:     true,
		ProjectID:   &projectID,
		Config:      map[string]string{},
		Features:    defaultFeatures(),
		Permissions: defaultPermissions(),
	}
	store, err := OpenStore(t.TempDir() + "/channels.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertInstance(instance); err != nil {
		t.Fatalf("seed failure channel: %v", err)
	}
	provider := &channelToolTestProvider{}
	manager := NewManager(store, nil)
	manager.RegisterFactory(instance.Type, func(ChannelInstance, EventSink) (ChannelProvider, error) {
		return provider, nil
	})
	if err := manager.Start(context.Background(), instance.ID); err != nil {
		t.Fatalf("start failure channel: %v", err)
	}
	defer manager.Stop(context.Background(), instance.ID)
	runtime := NewAgentRuntime(store, manager, nil, nil, nil, nil, func(context.Context, ChannelInstance) (services.PetProviderReference, error) {
		return services.PetProviderReference{}, errors.New("default model is unavailable")
	})

	runtime.handleIncoming(ChannelMessage{
		InstanceID: instance.ID,
		ExternalID: "incoming-failure",
		Role:       "user",
		ChatID:     "chat-a",
		Content:    "hello",
		Timestamp:  nowMillis(),
	})

	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.sent) != 1 || !strings.Contains(provider.sent[0], "客户端默认模型配置和 Relay 连接") {
		t.Fatalf("failure message = %#v", provider.sent)
	}
}
