package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var appVersionPattern = regexp.MustCompile(`(?m)^\s*const\s+AppVersion\s*=\s*"([^"]+)"`)

var versionMetadataChecks = []struct {
	path    string
	pattern *regexp.Regexp
}{
	{
		path:    "build/config.yml",
		pattern: regexp.MustCompile(`(?m)^\s+version:\s*"([^"]+)"\s+# The application version`),
	},
	{
		path:    "build/linux/nfpm/nfpm.yaml",
		pattern: regexp.MustCompile(`(?m)^version:\s*"([^"]+)"`),
	},
}

func main() {
	tag, err := resolveTag(os.Args[1:])
	if err != nil {
		fail(err)
	}

	if err := ensureVersionMetadata(tag); err != nil {
		fail(err)
	}
	if err := ensureCleanWorktree(); err != nil {
		fail(err)
	}
	if err := ensureTagAvailable(tag); err != nil {
		fail(err)
	}

	if err := runCommand("git", "tag", "-a", tag, "-m", "Release "+tag); err != nil {
		fail(fmt.Errorf("create tag %s: %w", tag, err))
	}
	if err := runCommand("git", "push", "origin", tag); err != nil {
		fail(fmt.Errorf("push tag %s: %w", tag, err))
	}

	fmt.Printf("Release tag %s pushed; GitHub Actions will build and publish the release.\n", tag)
}

func resolveTag(args []string) (string, error) {
	if len(args) > 1 {
		return "", fmt.Errorf("usage: go run ./scripts/publish.go [tag]")
	}

	if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
		return validateTag(strings.TrimSpace(args[0]))
	}

	content, err := os.ReadFile("version_service.go")
	if err != nil {
		return "", fmt.Errorf("read version_service.go: %w", err)
	}

	matches := appVersionPattern.FindSubmatch(content)
	if len(matches) != 2 {
		return "", fmt.Errorf("AppVersion was not found in version_service.go")
	}

	return validateTag(string(matches[1]))
}

func ensureVersionMetadata(tag string) error {
	expected := strings.TrimPrefix(tag, "v")

	// tag 只负责触发 CI，安装包版本仍来自这些配置文件；发布前强制保持一致，避免升级检查和安装器显示错版本。
	for _, check := range versionMetadataChecks {
		content, err := os.ReadFile(check.path)
		if err != nil {
			return fmt.Errorf("read %s: %w", check.path, err)
		}

		matches := check.pattern.FindSubmatch(content)
		if len(matches) != 2 {
			return fmt.Errorf("version metadata was not found in %s", check.path)
		}
		if string(matches[1]) != expected {
			return fmt.Errorf("version mismatch in %s: expected %s, got %s", check.path, expected, matches[1])
		}
	}

	return nil
}

func validateTag(tag string) (string, error) {
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`).MatchString(tag) {
		return "", fmt.Errorf("invalid release tag %q; expected a semantic version such as v0.1.9", tag)
	}
	return tag, nil
}

func ensureCleanWorktree() error {
	output, err := exec.Command("git", "status", "--porcelain").CombinedOutput()
	if err != nil {
		return fmt.Errorf("check git worktree: %w", err)
	}
	if strings.TrimSpace(string(output)) != "" {
		return fmt.Errorf("git worktree is not clean; commit or stash changes before publishing")
	}
	return nil
}

func ensureTagAvailable(tag string) error {
	if _, err := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/tags/"+tag).Output(); err == nil {
		return fmt.Errorf("tag %s already exists locally", tag)
	}

	output, err := exec.Command("git", "ls-remote", "--tags", "origin", "refs/tags/"+tag).CombinedOutput()
	if err != nil {
		return fmt.Errorf("check remote tag %s: %w", tag, err)
	}
	if strings.TrimSpace(string(output)) != "" {
		return fmt.Errorf("tag %s already exists on origin", tag)
	}
	return nil
}

func runCommand(name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
