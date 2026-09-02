//go:build windows

package services

import (
	"context"
	"strings"
	"testing"
)

type projectManagerAgentModelReaderStub struct {
	references []PetAgentModelReference
	petIDs     []string
	index      int
}

func (s *projectManagerAgentModelReaderStub) LoadAgentModelReference(_ context.Context, petID string) (PetAgentModelReference, error) {
	s.petIDs = append(s.petIDs, petID)
	if s.index >= len(s.references) {
		return PetAgentModelReference{}, nil
	}
	reference := s.references[s.index]
	s.index++
	return reference, nil
}

func TestProjectManagerAICommitReadsLatestAgentModelForEachLaunch(t *testing.T) {
	reader := &projectManagerAgentModelReaderStub{references: []PetAgentModelReference{
		{ProviderPlatform: "codex", ModelID: "gpt-first"},
		{ProviderPlatform: "codex", ModelID: "gpt-second"},
	}}
	service := NewProjectManagerServiceWithPetAgentModelReader(reader)

	first, err := service.loadProjectManagerAICommitModel()
	if err != nil {
		t.Fatalf("first model read error = %v", err)
	}
	second, err := service.loadProjectManagerAICommitModel()
	if err != nil {
		t.Fatalf("second model read error = %v", err)
	}
	if first.ModelID != "gpt-first" || second.ModelID != "gpt-second" {
		t.Fatalf("model reads = %#v, %#v", first, second)
	}
	if len(reader.petIDs) != 2 || reader.petIDs[0] != DefaultPetID || reader.petIDs[1] != DefaultPetID {
		t.Fatalf("pet IDs = %#v, want two default reads", reader.petIDs)
	}
}

func TestValidateProjectManagerAICommitModel(t *testing.T) {
	tests := []struct {
		name      string
		reference PetAgentModelReference
		want      string
		wantError string
	}{
		{
			name: "empty model keeps profile default",
			reference: PetAgentModelReference{
				ProviderPlatform: "openai",
				ModelID:          "  ",
			},
		},
		{
			name: "codex model",
			reference: PetAgentModelReference{
				ProviderPlatform: " CODEX ",
				ModelID:          " gpt-5-codex ",
			},
			want: "gpt-5-codex",
		},
		{
			name: "non codex platform",
			reference: PetAgentModelReference{
				ProviderPlatform: "openai",
				ModelID:          "gpt-4o",
			},
			wantError: "仅支持 codex",
		},
		{
			name: "newline",
			reference: PetAgentModelReference{
				ProviderPlatform: "codex",
				ModelID:          "gpt-5\nunsafe",
			},
			wantError: "不能包含 NUL 或换行",
		},
		{
			name: "nul",
			reference: PetAgentModelReference{
				ProviderPlatform: "codex",
				ModelID:          "gpt-5\x00unsafe",
			},
			wantError: "不能包含 NUL 或换行",
		},
		{
			name: "wildcard",
			reference: PetAgentModelReference{
				ProviderPlatform: "codex",
				ModelID:          "gpt-*",
			},
			wantError: "不能包含通配符",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := validateProjectManagerAICommitModel(test.reference)
			if test.wantError == "" {
				if err != nil || got != test.want {
					t.Fatalf("got = %q, error = %v, want %q", got, err, test.want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want substring %q", err, test.wantError)
			}
		})
	}
}

func TestBuildProjectManagerAICommitPowerShellCommandUsesOptionalModel(t *testing.T) {
	withoutModel := buildProjectManagerAICommitPowerShellCommand(`C:\work\repo`, "")
	if strings.Contains(withoutModel, "--model") {
		t.Fatalf("empty model command unexpectedly contains --model: %s", withoutModel)
	}
	if !strings.Contains(withoutModel, "-p commit-fast exec --ephemeral") {
		t.Fatalf("empty model command lost commit-fast invocation: %s", withoutModel)
	}

	withModel := buildProjectManagerAICommitPowerShellCommand(`C:\work\O'Brien`, "gpt'o")
	if !strings.Contains(withModel, "--model 'gpt''o'") {
		t.Fatalf("escaped model argument missing: %s", withModel)
	}
	if !strings.Contains(withModel, "Set-Location -LiteralPath 'C:\\work\\O''Brien'") {
		t.Fatalf("escaped project path missing: %s", withModel)
	}
	if strings.Index(withModel, "--model") > strings.Index(withModel, projectManagerAICommitPrompt) {
		t.Fatalf("model argument was appended after prompt: %s", withModel)
	}
}
