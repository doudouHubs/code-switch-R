package services

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"
)

// PetActivityPhase 是桌宠工作态的最小生命周期；output 只表示模型已经产生了
// 有效输出，不表示请求刚建立。这样前端不会把连接等待、usage 或空心跳误判成工作。
type PetActivityPhase string

const (
	PetActivityOutput    PetActivityPhase = "output"
	PetActivityCompleted PetActivityPhase = "completed"
	PetActivityFailed    PetActivityPhase = "failed"
	PetActivityCancelled PetActivityPhase = "cancelled"
)

// PetActivitySource 用于诊断事件来源；relay 没有绑定具体宠物时 PetID 可以为空。
type PetActivitySource string

const (
	PetActivitySourcePetAI PetActivitySource = "pet-ai"
	PetActivitySourceRelay PetActivitySource = "relay"
)

// PetActivityEvent 是桌宠工作态的跨 Wails/浏览器事件协议。
// Sequence 只在同一 requestId 内递增，前端可用它丢弃迟到的旧事件。
type PetActivityEvent struct {
	Phase     PetActivityPhase  `json:"phase"`
	PetID     string            `json:"petId,omitempty"`
	RequestID string            `json:"requestId"`
	Source    PetActivitySource `json:"source"`
	Sequence  int64             `json:"sequence"`
}

// PetActivityEmitter 是活动事件的最小输出边界。事件属于 UI 增强能力，
// emitter 失败不能反向打断模型请求或 provider 降级。
type PetActivityEmitter interface {
	Emit(event PetActivityEvent) error
}

// PetActivityEmitterFunc 方便宿主把同一事件同时发布到多个运行时通道。
type PetActivityEmitterFunc func(PetActivityEvent) error

func (f PetActivityEmitterFunc) Emit(event PetActivityEvent) error {
	if f == nil {
		return nil
	}
	return f(event)
}

// petActivityRequest 是单个外部模型请求的生命周期 owner。
// provider 尝试、重试和协议转换都只能调用 Output/Finish，不能各自结束请求。
type petActivityRequest struct {
	emitter PetActivityEmitter
	request PetActivityEvent

	mu           sync.Mutex
	sequence     int64
	outputSent   bool
	terminalSent bool
}

func newPetActivityRequest(emitter PetActivityEmitter, source PetActivitySource, requestID, petID string) *petActivityRequest {
	return &petActivityRequest{
		emitter: emitter,
		request: PetActivityEvent{
			RequestID: requestID,
			Source:    source,
			PetID:     petID,
		},
	}
}

func newPetActivityRequestID(prefix string) string {
	return prefix + ":" + uuid.NewString()
}

// Output 只允许首次有效输出打开工作态；重复 Token 不会反复触发前端状态切换。
func (r *petActivityRequest) Output() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.outputSent || r.terminalSent {
		r.mu.Unlock()
		return
	}
	r.outputSent = true
	event := r.nextEventLocked(PetActivityOutput)
	r.mu.Unlock()
	r.emit(event)
}

// Finish 只对已经产生过输出的请求发送终态；没有 Token 的失败请求不需要让
// 前端凭空删除一个不存在的工作状态。重复终态由 request owner 幂等丢弃。
func (r *petActivityRequest) Finish(phase PetActivityPhase) {
	if r == nil || !isPetActivityTerminal(phase) {
		return
	}
	r.mu.Lock()
	if !r.outputSent || r.terminalSent {
		r.mu.Unlock()
		return
	}
	r.terminalSent = true
	event := r.nextEventLocked(phase)
	r.mu.Unlock()
	r.emit(event)
}

func (r *petActivityRequest) nextEventLocked(phase PetActivityPhase) PetActivityEvent {
	r.sequence++
	event := r.request
	event.Phase = phase
	event.Sequence = r.sequence
	return event
}

func (r *petActivityRequest) emit(event PetActivityEvent) {
	if r == nil || r.emitter == nil {
		return
	}
	// 活动态是旁路 UI 提示；不要把广播失败传播给已经建立的模型请求。
	_ = r.emitter.Emit(event)
}

func isPetActivityTerminal(phase PetActivityPhase) bool {
	switch phase {
	case PetActivityCompleted, PetActivityFailed, PetActivityCancelled:
		return true
	default:
		return false
	}
}

func petActivityPhaseForResult(ctx context.Context, err error) PetActivityPhase {
	if errors.Is(err, context.Canceled) || (ctx != nil && errors.Is(ctx.Err(), context.Canceled)) {
		return PetActivityCancelled
	}
	if err != nil || (ctx != nil && ctx.Err() != nil) {
		return PetActivityFailed
	}
	return PetActivityCompleted
}
