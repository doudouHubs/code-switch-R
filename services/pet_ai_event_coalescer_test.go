package services

import (
	"testing"
	"time"
)

func TestPetAIEventCoalescerMergesDeltasAndFlushesBeforeTerminal(t *testing.T) {
	events := make(chan PetAIEvent, 8)
	coalescer := NewPetAIEventCoalescer(30*time.Millisecond, func(event PetAIEvent) {
		events <- event
	})
	defer coalescer.Close()

	coalescer.Submit(PetAIEvent{Type: PetAIEventStarted, PetID: "pet-1", RequestID: "request-1", Sequence: 1})
	coalescer.Submit(PetAIEvent{Type: PetAIEventDelta, PetID: "pet-1", RequestID: "request-1", Sequence: 2, Delta: "甲"})
	coalescer.Submit(PetAIEvent{Type: PetAIEventDelta, PetID: "pet-1", RequestID: "request-1", Sequence: 3, Delta: "乙"})
	coalescer.Submit(PetAIEvent{Type: PetAIEventCompleted, PetID: "pet-1", RequestID: "request-1", Sequence: 4, Text: "甲乙"})

	got := make([]PetAIEvent, 0, 3)
	for len(got) < 3 {
		select {
		case event := <-events:
			got = append(got, event)
		case <-time.After(time.Second):
			t.Fatalf("等待合并事件超时，已收到 %#v", got)
		}
	}
	if got[0].Type != PetAIEventStarted || got[1].Type != PetAIEventDelta || got[2].Type != PetAIEventCompleted {
		t.Fatalf("事件顺序 = %#v", got)
	}
	if got[1].Delta != "甲乙" || got[1].Sequence != 3 {
		t.Fatalf("合并 delta = %#v", got[1])
	}
}

func TestPetAIEventCoalescerFlushesDeltaWithoutTerminal(t *testing.T) {
	events := make(chan PetAIEvent, 2)
	coalescer := NewPetAIEventCoalescer(10*time.Millisecond, func(event PetAIEvent) {
		events <- event
	})
	defer coalescer.Close()

	coalescer.Submit(PetAIEvent{PetID: "pet-1", RequestID: "request-1", Type: PetAIEventDelta, Sequence: 2, Delta: "流式"})
	select {
	case event := <-events:
		if event.Type != PetAIEventDelta || event.Delta != "流式" {
			t.Fatalf("timer flush = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("delta timer flush 超时")
	}
}
