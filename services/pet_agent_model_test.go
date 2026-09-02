package services

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPetDAOLoadAgentModelReferenceReadsOnlyModelProjection(t *testing.T) {
	db := newPetMigrationTestDB(t)
	dao := NewPetDAO(db)

	payload, err := json.Marshal(map[string]any{
		"providerPlatform": "  CODEX  ",
		"providerId":       map[string]any{"unexpected": "shape"},
		"modelId":          "  gpt-5-codex  ",
		"reasoningEffort":  " HIGH ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO pet_agent (pet_id, config_json, updated_at) VALUES (?, ?, ?)`,
		DefaultPetID,
		string(payload),
		1,
	); err != nil {
		t.Fatal(err)
	}

	reference, err := dao.LoadAgentModelReference(context.Background(), "  ")
	if err != nil {
		t.Fatalf("LoadAgentModelReference() error = %v", err)
	}
	if reference.ProviderPlatform != "codex" || reference.ModelID != "gpt-5-codex" || reference.ReasoningEffort != PetReasoningHigh {
		t.Fatalf("reference = %#v", reference)
	}
}

func TestPetDAOLoadAgentModelReferenceReturnsEmptyWhenAgentIsMissing(t *testing.T) {
	dao := NewPetDAO(newPetMigrationTestDB(t))

	reference, err := dao.LoadAgentModelReference(context.Background(), "missing-pet")
	if err != nil {
		t.Fatalf("LoadAgentModelReference() error = %v", err)
	}
	if reference != (PetAgentModelReference{}) {
		t.Fatalf("reference = %#v, want empty reference", reference)
	}
}

func TestPetDAOLoadAgentModelReferenceReportsMalformedAgentJSON(t *testing.T) {
	db := newPetMigrationTestDB(t)
	dao := NewPetDAO(db)
	if _, err := db.Exec(
		`INSERT INTO pet_agent (pet_id, config_json, updated_at) VALUES (?, ?, ?)`,
		DefaultPetID,
		"{broken",
		1,
	); err != nil {
		t.Fatal(err)
	}

	_, err := dao.LoadAgentModelReference(context.Background(), DefaultPetID)
	if err == nil || !strings.Contains(err.Error(), "decode pet agent model reference") {
		t.Fatalf("error = %v, want malformed model reference error", err)
	}
}
