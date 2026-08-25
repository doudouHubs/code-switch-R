package channels

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type blockingStopProvider struct {
	channelToolTestProvider
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingStopProvider) Stop(context.Context) error {
	p.once.Do(func() { close(p.started) })
	<-p.release
	return nil
}

func TestManagerStopAllStopsProvidersInParallelAndHonorsContext(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "channels.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	manager := NewManager(store, nil)
	providers := make([]*blockingStopProvider, 0, 2)
	for _, id := range []string{"blocking-a", "blocking-b"} {
		projectID := "project-a"
		if err := store.UpsertInstance(ChannelInstance{
			ID: id, Type: "blocking", Name: id, Enabled: true, ProjectID: &projectID,
			Config: map[string]string{}, Status: "running",
		}); err != nil {
			t.Fatal(err)
		}
		provider := &blockingStopProvider{started: make(chan struct{}), release: make(chan struct{})}
		providers = append(providers, provider)
		manager.providers[id] = provider
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := make(chan struct{})
	go func() {
		for _, provider := range providers {
			<-provider.started
		}
		close(started)
	}()
	startedAt := time.Now()
	err = manager.StopAll(ctx)
	elapsed := time.Since(startedAt)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StopAll() error = %v, want context deadline", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("StopAll did not start all providers")
	}
	if elapsed > 700*time.Millisecond {
		t.Fatalf("StopAll() took %s after context deadline", elapsed)
	}

	// provider.Stop 可能不响应 context；释放测试 goroutine，验证 Manager 返回后不会
	// 把不可取消的旧实现继续挂在测试进程里。
	for _, provider := range providers {
		close(provider.release)
	}
}
