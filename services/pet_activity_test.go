package services

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestPetActivityRequestEmitsOutputAndSingleTerminal(t *testing.T) {
	var (
		mu     sync.Mutex
		events []PetActivityEvent
	)
	emitter := PetActivityEmitterFunc(func(event PetActivityEvent) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
		return nil
	})
	request := newPetActivityRequest(emitter, PetActivitySourceRelay, "relay:test", "")

	request.Finish(PetActivityCompleted)
	request.Output()
	request.Output()
	request.Finish(PetActivityCompleted)
	request.Finish(PetActivityFailed)

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("expected one output and one terminal event, got %d", len(events))
	}
	if events[0].Phase != PetActivityOutput || events[0].Sequence != 1 {
		t.Fatalf("unexpected output event: %+v", events[0])
	}
	if events[1].Phase != PetActivityCompleted || events[1].Sequence != 2 {
		t.Fatalf("unexpected terminal event: %+v", events[1])
	}
}

func TestPetActivityRequestWithoutOutputDoesNotEmitTerminal(t *testing.T) {
	var events []PetActivityEvent
	request := newPetActivityRequest(
		PetActivityEmitterFunc(func(event PetActivityEvent) error {
			events = append(events, event)
			return nil
		}),
		PetActivitySourcePetAI,
		"pet-ai:test",
		"pet-1",
	)

	request.Finish(PetActivityFailed)
	if len(events) != 0 {
		t.Fatalf("empty request must not emit activity events: %+v", events)
	}
}

func TestPetActivityPhaseForResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want PetActivityPhase
	}{
		{name: "success", ctx: context.Background(), want: PetActivityCompleted},
		{name: "error", ctx: context.Background(), err: errors.New("upstream"), want: PetActivityFailed},
		{name: "cancelled context", ctx: ctx, want: PetActivityCancelled},
		{name: "cancelled error", ctx: context.Background(), err: context.Canceled, want: PetActivityCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := petActivityPhaseForResult(tt.ctx, tt.err); got != tt.want {
				t.Fatalf("petActivityPhaseForResult() = %q, want %q", got, tt.want)
			}
		})
	}
}
