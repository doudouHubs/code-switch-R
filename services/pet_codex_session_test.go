package services

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestPetCodexSessionPersistsPerPetAndRoundTripsMetadata(t *testing.T) {
	dao := NewPetDAO(newPetMigrationTestDB(t))
	rootA := filepath.Join(t.TempDir(), "pet-a")
	rootB := filepath.Join(t.TempDir(), "pet-b")
	wantA := PetCodexSession{
		PetID:              "pet-a",
		ThreadID:           "thread-a",
		Workspace:          rootA,
		PersonaFingerprint: "fingerprint-a",
		ProtocolVersion:    PetCodexPlanProtocolVersion,
		UpdatedAt:          101,
	}
	wantB := PetCodexSession{
		PetID:              "pet-b",
		ThreadID:           "thread-b",
		Workspace:          rootB,
		PersonaFingerprint: "fingerprint-b",
		ProtocolVersion:    PetCodexPlanProtocolVersion,
		UpdatedAt:          202,
	}
	if err := dao.SaveCodexSession(context.Background(), wantA); err != nil {
		t.Fatalf("save pet-a session: %v", err)
	}
	if err := dao.SaveCodexSession(context.Background(), wantB); err != nil {
		t.Fatalf("save pet-b session: %v", err)
	}

	gotA, err := dao.LoadCodexSession(context.Background(), "pet-a")
	if err != nil {
		t.Fatalf("load pet-a session: %v", err)
	}
	gotB, err := dao.LoadCodexSession(context.Background(), "pet-b")
	if err != nil {
		t.Fatalf("load pet-b session: %v", err)
	}
	if gotA == nil || gotA.ThreadID != wantA.ThreadID || gotA.Workspace != wantA.Workspace || gotA.UpdatedAt != wantA.UpdatedAt {
		t.Fatalf("pet-a session = %#v, want %#v", gotA, wantA)
	}
	if gotB == nil || gotB.ThreadID != wantB.ThreadID || gotB.Workspace != wantB.Workspace || gotB.UpdatedAt != wantB.UpdatedAt {
		t.Fatalf("pet-b session = %#v, want %#v", gotB, wantB)
	}

	encoded, err := json.Marshal(gotA)
	if err != nil || len(encoded) == 0 {
		t.Fatalf("session JSON should remain serializable: %s, err=%v", encoded, err)
	}
	if err := dao.DeleteCodexSession(context.Background(), "pet-a"); err != nil {
		t.Fatalf("delete pet-a session: %v", err)
	}
	deleted, err := dao.LoadCodexSession(context.Background(), "pet-a")
	if err != nil {
		t.Fatalf("load deleted pet-a session: %v", err)
	}
	if deleted != nil {
		t.Fatalf("deleted pet-a session = %#v", deleted)
	}
	remaining, err := dao.LoadCodexSession(context.Background(), "pet-b")
	if err != nil || remaining == nil || remaining.ThreadID != wantB.ThreadID {
		t.Fatalf("pet-b session was affected by pet-a delete: %#v, err=%v", remaining, err)
	}
}

func TestParseAndVerifyPetCodexThreadResponseUsesThreadCWDAsBoundary(t *testing.T) {
	workspace := filepath.Clean(filepath.Join(t.TempDir(), "workspace"))
	valid := map[string]any{
		"thread": map[string]any{"id": "thread-1", "cwd": workspace},
		"cwd":    workspace,
		// 这些字段刻意使用任意策略，证明宠物 runtime 不会把 Codex 默认配置
		// 重新解释成自己的固定策略；安全校验只看 thread 的工作目录。
		"approvalPolicy": "on-request",
		"sandbox": map[string]any{
			"type":          "readOnly",
			"writableRoots": []string{},
			"networkAccess": false,
		},
	}
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseAndVerifyPetCodexThreadResponse(raw, workspace, ""); err != nil {
		t.Fatalf("valid Codex thread response rejected: %v", err)
	}

	unsafe := map[string]any{}
	for key, value := range valid {
		unsafe[key] = value
	}
	unsafe["thread"] = map[string]any{"id": "thread-1", "cwd": filepath.Join(workspace, "outside")}
	raw, err = json.Marshal(unsafe)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseAndVerifyPetCodexThreadResponse(raw, workspace, ""); err == nil {
		t.Fatal("thread outside the bound workspace should be rejected")
	}

	missingDefaults := map[string]any{
		"thread": map[string]any{"id": "thread-2", "cwd": workspace},
	}
	raw, err = json.Marshal(missingDefaults)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseAndVerifyPetCodexThreadResponse(raw, workspace, ""); err != nil {
		t.Fatalf("response without projected default fields rejected: %v", err)
	}
}
