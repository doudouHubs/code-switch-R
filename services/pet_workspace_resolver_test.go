package services

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

type petProjectAgentReaderStub struct {
	agents map[string]*PetAgentConfig
}

func (s *petProjectAgentReaderStub) LoadAgent(_ context.Context, petID string) (*PetAgentConfig, error) {
	agent := s.agents[petID]
	if agent == nil {
		return nil, nil
	}
	copy := *agent
	return &copy, nil
}

type petProjectReaderStub struct {
	projects []ProjectSummary
}

func (s *petProjectReaderStub) ListProjects() ([]ProjectSummary, error) {
	return append([]ProjectSummary(nil), s.projects...), nil
}

func TestPetProjectWorkspaceResolverUsesProjectIDWhenProjectFolderIsEmpty(t *testing.T) {
	workspace := t.TempDir()
	projectID := filepath.Join(workspace, "..", "project")
	resolver := NewPetProjectWorkspaceResolver(
		&petProjectAgentReaderStub{agents: map[string]*PetAgentConfig{
			"pet-a": {
				PetID:         "pet-a",
				ProjectID:     petProjectStringPtr(projectID),
				ProjectFolder: nil,
			},
		}},
		&petProjectReaderStub{projects: []ProjectSummary{
			{ID: projectID, Path: workspace},
		}},
	)

	got, err := resolver.Resolve(context.Background(), "pet-a")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got != filepath.Clean(workspace) {
		t.Fatalf("Resolve() = %q, want %q", got, filepath.Clean(workspace))
	}
}

func TestPetProjectWorkspaceResolverDoesNotFallbackWhenProjectIDIsMissing(t *testing.T) {
	legacyWorkspace := t.TempDir()
	resolver := NewPetProjectWorkspaceResolver(
		&petProjectAgentReaderStub{agents: map[string]*PetAgentConfig{
			"pet-stale": {
				PetID:         "pet-stale",
				ProjectID:     petProjectStringPtr("project-that-was-removed"),
				ProjectFolder: petProjectStringPtr(legacyWorkspace),
			},
		}},
		&petProjectReaderStub{},
	)

	if _, err := resolver.Resolve(context.Background(), "pet-stale"); err == nil {
		t.Fatal("Resolve() should reject a missing project instead of using projectFolder")
	}
}

func TestPetProjectWorkspaceResolverKeepsLegacyProjectFolderCompatibility(t *testing.T) {
	legacyWorkspace := t.TempDir()
	resolver := NewPetProjectWorkspaceResolver(
		&petProjectAgentReaderStub{agents: map[string]*PetAgentConfig{
			"pet-legacy": {
				PetID:         "pet-legacy",
				ProjectFolder: petProjectStringPtr(legacyWorkspace),
			},
		}},
		nil,
	)

	got, err := resolver.Resolve(context.Background(), "pet-legacy")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got != filepath.Clean(legacyWorkspace) {
		t.Fatalf("Resolve() = %q, want %q", got, filepath.Clean(legacyWorkspace))
	}
}

func TestPetProjectWorkspaceResolverIsolatesPets(t *testing.T) {
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	projectA := "project-a"
	projectB := "project-b"
	resolver := NewPetProjectWorkspaceResolver(
		&petProjectAgentReaderStub{agents: map[string]*PetAgentConfig{
			"pet-a": {PetID: "pet-a", ProjectID: petProjectStringPtr(projectA)},
			"pet-b": {PetID: "pet-b", ProjectID: petProjectStringPtr(projectB)},
		}},
		&petProjectReaderStub{projects: []ProjectSummary{
			{ID: projectA, Path: workspaceA},
			{ID: projectB, Path: workspaceB},
		}},
	)

	for _, testCase := range []struct {
		petID string
		want  string
	}{
		{petID: "pet-a", want: filepath.Clean(workspaceA)},
		{petID: "pet-b", want: filepath.Clean(workspaceB)},
	} {
		t.Run(testCase.petID, func(t *testing.T) {
			got, err := resolver.Resolve(context.Background(), testCase.petID)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got != testCase.want {
				t.Fatalf("Resolve() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestPetCodexRuntimeWorkspaceBindingErrorsAreStable(t *testing.T) {
	projectID := "missing-project"
	resolver := NewPetProjectWorkspaceResolver(
		&petProjectAgentReaderStub{agents: map[string]*PetAgentConfig{
			"pet-missing": {PetID: "pet-missing", ProjectID: petProjectStringPtr(projectID)},
		}},
		&petProjectReaderStub{},
	)
	runtime := &PetCodexRuntime{workspaceResolver: resolver}

	if _, err := runtime.resolveWorkspace(context.Background(), "pet-missing"); PetAIErrorCodeOf(err) != string(PET_AI_WORKSPACE_UNAVAILABLE) {
		t.Fatalf("missing project error = %v, want %s", err, PET_AI_WORKSPACE_UNAVAILABLE)
	}
}

func TestPetCodexRuntimeStartsTurnWithProjectIDWorkspace(t *testing.T) {
	workspace := t.TempDir()
	projectID := "project-codex"
	resolver := NewPetProjectWorkspaceResolver(
		&petProjectAgentReaderStub{agents: map[string]*PetAgentConfig{
			"pet-codex": {
				PetID:         "pet-codex",
				ProjectID:     petProjectStringPtr(projectID),
				ProjectFolder: petProjectStringPtr(t.TempDir()),
			},
		}},
		&petProjectReaderStub{projects: []ProjectSummary{
			{ID: projectID, Path: workspace},
		}},
	)
	sessions := &petCodexSessionMemory{sessions: make(map[string]PetCodexSession)}
	recorder := &petCodexEventRecorder{}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions:          sessions,
		WorkspaceResolver: resolver,
		Emitter:           recorder,
		CommandFactory:    newCodexFixtureFactory("pet-complete", func() string { return "project-thread" }),
		ResponseTimeout:   2 * time.Second,
	})
	defer runtime.Close()

	request := petCodexRuntimeRequest("pet-codex", "project-request", "项目绑定 persona")
	request.ProjectFolder = t.TempDir()
	if _, err := runtime.StartChat(context.Background(), request); err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	if completed := recorder.waitFor(request.RequestID, PetAIEventCompleted); completed.Text != "宠物回复" {
		t.Fatalf("completed event = %#v", completed)
	}
	sessions.mu.Lock()
	session := sessions.sessions[request.PetID]
	sessions.mu.Unlock()
	if session.Workspace != filepath.Clean(workspace) {
		t.Fatalf("saved Codex workspace = %q, want %q", session.Workspace, filepath.Clean(workspace))
	}
}

func TestPetCodexRuntimeRejectsMissingOrUnsafeProjectPath(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "missing", path: filepath.Join(t.TempDir(), "does-not-exist")},
		{name: "relative", path: filepath.Join("relative", "workspace")},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			projectID := "project-" + testCase.name
			resolver := NewPetProjectWorkspaceResolver(
				&petProjectAgentReaderStub{agents: map[string]*PetAgentConfig{
					"pet-path": {PetID: "pet-path", ProjectID: petProjectStringPtr(projectID)},
				}},
				&petProjectReaderStub{projects: []ProjectSummary{
					{ID: projectID, Path: testCase.path},
				}},
			)
			runtime := &PetCodexRuntime{workspaceResolver: resolver}
			if _, err := runtime.resolveWorkspace(context.Background(), "pet-path"); PetAIErrorCodeOf(err) != string(PET_AI_WORKSPACE_UNAVAILABLE) {
				t.Fatalf("workspace path error = %v, want %s", err, PET_AI_WORKSPACE_UNAVAILABLE)
			}
		})
	}
}

func petProjectStringPtr(value string) *string {
	return &value
}
