package services

import (
	"strings"
	"sync"
	"time"
)

const defaultPetAIEventCoalescingInterval = 50 * time.Millisecond

// PetAIEventSink 是宿主 UI 事件的最小输出边界。实现方应保持轻量，不能在
// sink 内同步等待模型或数据库；这样合并器才能只负责事件节流，不接管业务状态。
type PetAIEventSink func(PetAIEvent)

type PetAIEventCoalescer struct {
	interval time.Duration
	sink     PetAIEventSink

	mu      sync.Mutex
	closed  bool
	pending map[string]*petAIEventDeltaBatch

	// Wails Event.Emit 本身会为每次广播创建异步任务。这里单独串行化 sink，
	// 确保 timer flush 与 completed flush 不会交叉，终态不会跑到最后一段 delta 前面。
	dispatchMu sync.Mutex
}

type petAIEventDeltaBatch struct {
	event PetAIEvent
	timer *time.Timer
}

// NewPetAIEventCoalescer 创建一个只合并连续 delta 的宿主事件适配器。默认
// 20Hz 足以保持流式手感，同时把每 token 一次的 Wails 全窗口广播降到有界频率。
func NewPetAIEventCoalescer(interval time.Duration, sink PetAIEventSink) *PetAIEventCoalescer {
	if interval <= 0 {
		interval = defaultPetAIEventCoalescingInterval
	}
	return &PetAIEventCoalescer{
		interval: interval,
		sink:     sink,
		pending:  make(map[string]*petAIEventDeltaBatch),
	}
}

// Submit 接收原始事件。连续 delta 只合并正文和最新 sequence；非 delta 事件
// 会先冲刷同一 request 的待发文本，从而保留 started 到 terminal 的生命周期顺序。
func (c *PetAIEventCoalescer) Submit(event PetAIEvent) {
	if c == nil || c.sink == nil {
		return
	}
	requestID := strings.TrimSpace(event.RequestID)
	if event.Type != PetAIEventDelta || requestID == "" || event.Delta == "" {
		c.dispatchWithFlush(requestID, &event)
		return
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	batch := c.pending[requestID]
	if batch == nil {
		batch = &petAIEventDeltaBatch{event: event}
		c.pending[requestID] = batch
		batch.timer = time.AfterFunc(c.interval, func() {
			c.flush(requestID)
		})
	} else {
		batch.event.Delta += event.Delta
		batch.event.Sequence = event.Sequence
		batch.event.PetID = event.PetID
		if event.Text != "" {
			batch.event.Text = event.Text
		}
	}
	c.mu.Unlock()
}

func (c *PetAIEventCoalescer) flush(requestID string) {
	if c == nil {
		return
	}
	c.dispatchMu.Lock()
	defer c.dispatchMu.Unlock()
	c.dispatchPending(requestID)
}

func (c *PetAIEventCoalescer) dispatchWithFlush(requestID string, event *PetAIEvent) {
	c.dispatchMu.Lock()
	defer c.dispatchMu.Unlock()
	c.dispatchPending(requestID)

	c.mu.Lock()
	closed := c.closed
	sink := c.sink
	c.mu.Unlock()
	if !closed && sink != nil && event != nil {
		sink(*event)
	}
}

func (c *PetAIEventCoalescer) dispatchPending(requestID string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	batch := c.pending[requestID]
	if batch != nil {
		delete(c.pending, requestID)
		if batch.timer != nil {
			batch.timer.Stop()
		}
	}
	sink := c.sink
	c.mu.Unlock()
	if batch != nil && sink != nil {
		sink(batch.event)
	}
}

// Close 停止尚未触发的 timer 并丢弃剩余 UI 旁路事件。应用关闭时模型和窗口
// 已进入收口阶段，继续向已销毁 WebView 广播只会制造新的异步任务。
func (c *PetAIEventCoalescer) Close() {
	if c == nil {
		return
	}
	c.dispatchMu.Lock()
	defer c.dispatchMu.Unlock()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	for requestID, batch := range c.pending {
		if batch != nil && batch.timer != nil {
			batch.timer.Stop()
		}
		delete(c.pending, requestID)
	}
	c.mu.Unlock()
}
