package services

import "testing"

func TestGeminiPathsRejectRelativeHome(t *testing.T) {
	t.Setenv("HOME", ".")
	t.Setenv("USERPROFILE", ".")

	if _, err := getGeminiDir(); err == nil {
		t.Fatal("getGeminiDir should reject a relative home directory")
	}
	if _, err := getGeminiProvidersPath(); err == nil {
		t.Fatal("getGeminiProvidersPath should reject a relative home directory")
	}
	if err := writeGeminiEnv(map[string]string{"GEMINI_API_KEY": "test"}); err == nil {
		t.Fatal("writeGeminiEnv should not write when the home directory is invalid")
	}
}

func TestCustomCliServiceRejectsRelativeHome(t *testing.T) {
	t.Setenv("HOME", ".")
	t.Setenv("USERPROFILE", ".")

	service := NewCustomCliService(":18100")
	if _, err := service.expandPath("~/.custom-cli/config.json"); err == nil {
		t.Fatal("expandPath should reject a relative home directory")
	}

	_, err := service.CreateTool(CustomCliTool{
		Name: "test-tool",
		ConfigFiles: []ConfigFile{
			{Path: "~/test-tool.json", Format: "json"},
		},
	})
	if err == nil {
		t.Fatal("CreateTool should fail instead of writing to the working directory")
	}
}
