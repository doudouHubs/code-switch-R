package channels

import (
	"strings"
	"testing"
)

func TestRemoveInstanceStopsAndDeletesOnlyCustomActiveChannel(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/channels.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	manager := NewManager(store, nil)
	service := NewChannelService(store, manager, nil)
	if err := store.UpsertInstance(ChannelInstance{ID: "custom-channel", Type: "custom", Name: "Custom", Config: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if err := service.RemoveInstance("custom-channel"); err != nil {
		t.Fatalf("remove custom channel: %v", err)
	}
	if _, found, err := store.GetInstance("custom-channel"); err != nil || found {
		t.Fatalf("custom channel still exists: found=%t err=%v", found, err)
	}

	if err := store.UpsertInstance(ChannelInstance{ID: "builtin-channel", Type: ChannelTypeDiscord, Name: "Discord", Builtin: true}); err != nil {
		t.Fatal(err)
	}
	if err := service.RemoveInstance("builtin-channel"); err == nil || !strings.Contains(err.Error(), "builtin channel cannot be removed") {
		t.Fatalf("builtin remove error = %v", err)
	}

}
