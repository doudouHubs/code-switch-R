package services

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToggleSkillInjectionCreatesCodexMetadataWhenMissing(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}

	projectRoot := t.TempDir()
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("chdir temp project failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(cwd)
	}()

	skillDir := filepath.Join(projectRoot, ".codex", "skills", "demo-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# demo\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md failed: %v", err)
	}

	ss := NewSkillService()
	if err := ss.ToggleSkillInjection("demo-skill", skillPlatformCodex, skillLocationProject, false); err != nil {
		t.Fatalf("toggle injection failed: %v", err)
	}

	metadataPath := filepath.Join(skillDir, "agents", "openai.yaml")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read metadata failed: %v", err)
	}

	expected := "policy:\n  allow_implicit_invocation: false\n"
	if string(data) != expected {
		t.Fatalf("unexpected metadata content:\nwant:\n%s\ngot:\n%s", expected, string(data))
	}
}
