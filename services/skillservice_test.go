package services

import (
	"os"
	"path/filepath"
	"strings"
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

func TestListSkillsForPlatformIncludesCodexPluginSkills(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}

	home := t.TempDir()
	projectRoot := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("chdir temp project failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(cwd)
	}()

	writeTestSkill(t, filepath.Join(home, ".codex", "skills", "same-name"), "User Same Name")
	writeTestSkill(t, filepath.Join(home, ".codex", "plugins", "cache", "market", "demo-plugin", "1.0.0", "skills", "plugin-direct"), "Direct Plugin")
	writeTestSkill(t, filepath.Join(home, ".codex", "plugins", "cache", "market", "demo-plugin", "1.0.0", ".codex", "skills", "plugin-nested"), "Nested Plugin")
	writeTestSkill(t, filepath.Join(home, ".codex", "plugins", "cache", "market", "demo-plugin", "1.0.0", "skills", "same-name"), "Plugin Same Name")

	ss := NewSkillService()
	skills, err := ss.ListSkillsForPlatform(skillPlatformCodex)
	if err != nil {
		t.Fatalf("list codex skills failed: %v", err)
	}

	byKey := map[string]Skill{}
	for _, skill := range skills {
		byKey[skill.Key] = skill
	}

	assertPluginSkill := func(key, name string) {
		t.Helper()
		skill, ok := byKey[key]
		if !ok {
			t.Fatalf("missing plugin skill key %s; got keys: %#v", key, byKey)
		}
		if skill.Name != name {
			t.Fatalf("plugin skill %s name mismatch: want %q got %q", key, name, skill.Name)
		}
		if skill.InstallLocation != skillLocationPlugin {
			t.Fatalf("plugin skill %s install location mismatch: %s", key, skill.InstallLocation)
		}
		if !skill.Readonly {
			t.Fatalf("plugin skill %s should be readonly", key)
		}
		if skill.PluginSource != "market" || skill.PluginName != "demo-plugin" || skill.PluginVersion != "1.0.0" {
			t.Fatalf("plugin source mismatch: %#v", skill)
		}
	}

	assertPluginSkill("codex:plugin:market:demo-plugin:1.0.0:plugin-direct", "Direct Plugin")
	assertPluginSkill("codex:plugin:market:demo-plugin:1.0.0:plugin-nested", "Nested Plugin")
	assertPluginSkill("codex:plugin:market:demo-plugin:1.0.0:same-name", "Plugin Same Name")

	userSkill, ok := byKey["codex:user:same-name"]
	if !ok {
		t.Fatalf("missing user skill with same directory")
	}
	if userSkill.InstallLocation != skillLocationUser || userSkill.Readonly {
		t.Fatalf("user skill should remain writable user skill: %#v", userSkill)
	}
}

func TestListSkillsForPlatformUsesLatestCodexPluginCacheVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	oldSkillPath := filepath.Join(home, ".codex", "plugins", "cache", "market", "demo-plugin", "1.9.0", "skills", "versioned")
	newSkillPath := filepath.Join(home, ".codex", "plugins", "cache", "market", "demo-plugin", "1.10.0", "skills", "versioned")
	writeTestSkill(t, oldSkillPath, "Old Plugin Skill")
	writeTestSkill(t, newSkillPath, "New Plugin Skill")

	ss := NewSkillService()
	skills, err := ss.ListSkillsForPlatform(skillPlatformCodex)
	if err != nil {
		t.Fatalf("list Codex skills failed: %v", err)
	}

	pluginSkills := make([]Skill, 0, 1)
	for _, skill := range skills {
		if skill.PluginName == "demo-plugin" && skill.Directory == "versioned" {
			pluginSkills = append(pluginSkills, skill)
		}
	}
	if len(pluginSkills) != 1 {
		t.Fatalf("同一 plugin 的缓存旧版本不应重复展示，got=%#v", pluginSkills)
	}
	if pluginSkills[0].PluginVersion != "1.10.0" || pluginSkills[0].Name != "New Plugin Skill" {
		t.Fatalf("应展示最高 plugin 版本，got=%#v", pluginSkills[0])
	}
}

func TestGetSkillContentReadsCodexPluginSkillByKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	skillPath := filepath.Join(home, ".codex", "plugins", "cache", "market", "demo-plugin", "1.0.0", "skills", "plugin-direct")
	writeTestSkill(t, skillPath, "Direct Plugin")

	ss := NewSkillService()
	content, err := ss.GetSkillContent("codex:plugin:market:demo-plugin:1.0.0:plugin-direct", skillPlatformCodex, skillLocationPlugin)
	if err != nil {
		t.Fatalf("get plugin skill content failed: %v", err)
	}
	if content == "" || !strings.Contains(content, "Direct Plugin") {
		t.Fatalf("unexpected plugin skill content: %q", content)
	}

	if _, err := ss.GetSkillContent("codex:plugin:market:demo-plugin:1.0.0:..", skillPlatformCodex, skillLocationPlugin); err == nil {
		t.Fatalf("expected invalid plugin key to be rejected")
	}
}

func TestToggleSkillInjectionSupportsCodexPluginSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	skillPath := filepath.Join(home, ".codex", "plugins", "cache", "market", "demo-plugin", "1.0.0", "skills", "plugin-direct")
	writeTestSkill(t, skillPath, "Direct Plugin")

	ss := NewSkillService()
	key := "codex:plugin:market:demo-plugin:1.0.0:plugin-direct"
	if err := ss.ToggleSkillInjection(key, skillPlatformCodex, skillLocationPlugin, false); err != nil {
		t.Fatalf("toggle plugin injection failed: %v", err)
	}

	metadataPath := filepath.Join(skillPath, "agents", "openai.yaml")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read plugin openai.yaml failed: %v", err)
	}
	expected := "policy:\n  allow_implicit_invocation: false\n"
	if string(data) != expected {
		t.Fatalf("unexpected plugin metadata content:\nwant:\n%s\ngot:\n%s", expected, string(data))
	}

	skills, err := ss.ListSkillsForPlatform(skillPlatformCodex)
	if err != nil {
		t.Fatalf("list codex skills failed: %v", err)
	}
	for _, skill := range skills {
		if skill.Key == key {
			if skill.InjectEnabled {
				t.Fatalf("plugin skill inject flag should reflect openai.yaml override: %#v", skill)
			}
			return
		}
	}
	t.Fatalf("missing plugin skill after toggling injection")
}

func writeTestSkill(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir failed: %v", err)
	}
	content := "---\nname: " + name + "\ndescription: test skill\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md failed: %v", err)
	}
}
