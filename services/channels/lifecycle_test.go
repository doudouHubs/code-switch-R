package channels

import (
	"context"
	"strings"
	"testing"
)

func TestArchivedChannelIsReadOnlyAcrossLifecycle(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/channels.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	projectID := "project-a"
	instance := ChannelInstance{
		ID: "archived-channel", Type: ChannelTypeDiscord, Name: "Archived Discord", Builtin: true,
		Archived: true, ProjectID: &projectID, Config: map[string]string{"token": "history-only"}, Status: "stopped",
	}
	if err := store.UpsertInstance(instance); err != nil {
		t.Fatalf("seed archived channel: %v", err)
	}
	manager := NewManager(store, nil)
	manager.RegisterFactory(instance.Type, func(ChannelInstance, EventSink) (ChannelProvider, error) {
		return &channelToolTestProvider{}, nil
	})
	service := NewChannelService(store, manager, nil)

	assertReadOnly := func(name string, action func() error) {
		t.Helper()
		if err := action(); err == nil || !strings.Contains(err.Error(), "archived channel is read-only") {
			t.Fatalf("%s error = %v, want archived read-only error", name, err)
		}
	}
	assertReadOnly("save archived payload", func() error { return service.SaveInstance(instance) })
	editablePayload := instance
	editablePayload.Archived = false
	assertReadOnly("save archived record", func() error { return service.SaveInstance(editablePayload) })
	assertReadOnly("bind project", func() error { return service.BindProject(instance.ID, nil) })
	assertReadOnly("disable channel", func() error { return service.SetEnabled(instance.ID, false) })
	assertReadOnly("start channel", func() error { return service.Start(instance.ID) })
	assertReadOnly("stop channel", func() error { return service.Stop(instance.ID) })
	assertReadOnly("send message", func() error {
		_, err := service.SendMessage(instance.ID, "chat-a", "must reject")
		return err
	})
	assertReadOnly("list groups", func() error {
		_, err := service.ListGroups(instance.ID)
		return err
	})

	started, failed, err := manager.StartAuto(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(started) != 0 || len(failed) != 0 {
		t.Fatalf("archived channel entered auto-start: started=%v failed=%v", started, failed)
	}
}

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

	if err := store.UpsertInstance(ChannelInstance{ID: "archived-channel", Type: "custom", Name: "Archived", Archived: true}); err != nil {
		t.Fatal(err)
	}
	if err := service.RemoveInstance("archived-channel"); err == nil || !strings.Contains(err.Error(), "archived channel is read-only") {
		t.Fatalf("archived remove error = %v", err)
	}
}
