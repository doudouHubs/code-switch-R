package services

import (
	"strings"
	"sync"
)

// PetAICompletionBroker 只转发 AI 终态事件。精确 request waiter 使用缓冲 1 的
// channel，Publish 不等待消费者，因此不会让 Wails 或浏览器慢路径阻塞模型事件。
type PetAICompletionBroker struct {
	mu      sync.Mutex
	nextID  uint64
	waiters map[string]map[uint64]chan PetAIEvent
}

func NewPetAICompletionBroker() *PetAICompletionBroker {
	return &PetAICompletionBroker{waiters: make(map[string]map[uint64]chan PetAIEvent)}
}

func (b *PetAICompletionBroker) Register(requestID string) (<-chan PetAIEvent, func()) {
	channel := make(chan PetAIEvent, 1)
	requestID = strings.TrimSpace(requestID)
	if b == nil || requestID == "" {
		return channel, func() {}
	}
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	if b.waiters == nil {
		b.waiters = make(map[string]map[uint64]chan PetAIEvent)
	}
	if b.waiters[requestID] == nil {
		b.waiters[requestID] = make(map[uint64]chan PetAIEvent)
	}
	b.waiters[requestID][id] = channel
	b.mu.Unlock()
	return channel, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		waiters := b.waiters[requestID]
		delete(waiters, id)
		if len(waiters) == 0 {
			delete(b.waiters, requestID)
		}
	}
}

func (b *PetAICompletionBroker) Publish(event PetAIEvent) {
	if b == nil || event.Type != PetAIEventCompleted && event.Type != PetAIEventFailed && event.Type != PetAIEventCancelled {
		return
	}
	requestID := strings.TrimSpace(event.RequestID)
	if requestID == "" {
		return
	}
	b.mu.Lock()
	waiters := b.waiters[requestID]
	channels := make([]chan PetAIEvent, 0, len(waiters))
	for _, channel := range waiters {
		channels = append(channels, channel)
	}
	b.mu.Unlock()
	for _, channel := range channels {
		select {
		case channel <- event:
		default:
		}
	}
}
