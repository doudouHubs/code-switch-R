package services

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

const (
	skillStoreDir  = ".code-switch"
	skillStoreFile = "skill.json"

	// 平台常量
	skillPlatformClaude = "claude"
	skillPlatformCodex  = "codex"

	// 安装位置常量
	skillLocationUser    = "user"
	skillLocationProject = "project"
	skillLocationPlugin  = "plugin"
)

var (
	defaultRepoBranches = []string{"main", "master"}
	defaultSkillRepos   = []skillRepoConfig{
		{Owner: "ComposioHQ", Name: "awesome-claude-skills", Branch: "main", Enabled: true},
		{Owner: "anthropics", Name: "skills", Branch: "main", Enabled: true},
	}
)

type Skill struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Directory   string `json:"directory"`
	ReadmeURL   string `json:"readme_url"`
	Installed   bool   `json:"installed"`

	// 新增字段
	Enabled         bool   `json:"enabled"`                    // 是否启用
	InjectEnabled   bool   `json:"inject_enabled"`             // 是否允许自动注入
	LicenseFile     string `json:"license_file,omitempty"`     // 许可证文件路径
	Platform        string `json:"platform,omitempty"`         // "claude" | "codex"
	InstallLocation string `json:"install_location,omitempty"` // "user" | "project"
	Readonly        bool   `json:"readonly,omitempty"`         // 是否只读

	// 仓库字段
	RepoOwner  string `json:"repo_owner,omitempty"`
	RepoName   string `json:"repo_name,omitempty"`
	RepoBranch string `json:"repo_branch,omitempty"`

	// Codex plugin 缓存来源字段
	PluginSource  string `json:"plugin_source,omitempty"`
	PluginName    string `json:"plugin_name,omitempty"`
	PluginVersion string `json:"plugin_version,omitempty"`
}

type skillMetadata struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// skillMetadataExtended 扩展的元数据结构（包含 enabled 状态相关字段）
type skillMetadataExtended struct {
	Name                   string `yaml:"name"`
	Description            string `yaml:"description"`
	DisableModelInvocation bool   `yaml:"disable-model-invocation"`
	UserInvocable          *bool  `yaml:"user-invocable"`
	Policy                 struct {
		AllowImplicitInvocation *bool `yaml:"allow_implicit_invocation"`
	} `yaml:"policy"`
}

type codexSkillMetadataFile struct {
	Interface struct {
		DisplayName      string `yaml:"display_name"`
		ShortDescription string `yaml:"short_description"`
		DefaultPrompt    string `yaml:"default_prompt"`
	} `yaml:"interface"`
	Policy struct {
		AllowImplicitInvocation *bool `yaml:"allow_implicit_invocation"`
	} `yaml:"policy"`
}

type codexSkillConfigEntry struct {
	Path    string `toml:"path,omitempty"`
	Name    string `toml:"name,omitempty"`
	Enabled bool   `toml:"enabled"`
}

type skillStore struct {
	Skills map[string]skillState `json:"skills"`
	Repos  []skillRepoConfig     `json:"repos"`
}

type skillState struct {
	Installed   bool      `json:"installed"`
	InstalledAt time.Time `json:"installed_at,omitempty"`
}

type skillRepoConfig struct {
	Owner   string `json:"owner"`
	Name    string `json:"name"`
	Branch  string `json:"branch"`
	Enabled bool   `json:"enabled"`
}

type installRequest struct {
	Directory string `json:"directory"`
	RepoOwner string `json:"repo_owner"`
	RepoName  string `json:"repo_name"`
	Branch    string `json:"repo_branch"`
	Platform  string `json:"platform"` // "claude" | "codex"
	Location  string `json:"location"` // "user" | "project"
}

type SkillService struct {
	httpClient *http.Client
	storePath  string
	installDir string
	initErr    error
	mu         sync.Mutex
}

func NewSkillService() *SkillService {
	service := &SkillService{
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
	home, err := getUserHomeDir()
	if err != nil {
		// 技能目录和状态文件属于用户数据；没有可靠家目录时拒绝操作，避免污染当前项目。
		service.initErr = fmt.Errorf("初始化技能目录失败: %w", err)
		return service
	}
	service.storePath = filepath.Join(home, skillStoreDir, skillStoreFile)
	service.installDir = filepath.Join(home, ".claude", "skills")
	return service
}

func (ss *SkillService) requireUserStorage() error {
	if ss == nil {
		return errors.New("skill service is not initialized")
	}
	return ss.initErr
}

// getInstallPath 根据平台和位置返回 skills 目录路径
// platform: "claude" | "codex"
// location: "user" | "project"
func (ss *SkillService) getInstallPath(platform, location string) (string, error) {
	var basePath string

	switch location {
	case skillLocationProject:
		// 项目级: 使用当前工作目录
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("获取工作目录失败: %w", err)
		}
		basePath = cwd
	case skillLocationPlugin:
		return "", errors.New("plugin 技能属于只读缓存，不支持作为安装目录使用")
	case skillLocationUser:
		fallthrough
	default:
		// 用户级: 使用 home 目录
		home, err := getUserHomeDir()
		if err != nil {
			return "", fmt.Errorf("获取用户目录失败: %w", err)
		}
		basePath = home
	}

	var configDir string
	switch platform {
	case skillPlatformCodex:
		configDir = ".codex"
	case skillPlatformClaude:
		fallthrough
	default:
		configDir = ".claude"
	}

	return filepath.Join(basePath, configDir, "skills"), nil
}

// ListSkillsForPlatform 列出指定平台的技能（用户级 + 项目级）
func (ss *SkillService) ListSkillsForPlatform(platform string) ([]Skill, error) {
	if platform == "" {
		platform = skillPlatformClaude
	}

	var allSkills []Skill
	codexEnabledByPath := map[string]bool{}
	if platform == skillPlatformCodex {
		for _, location := range []string{skillLocationUser, skillLocationProject} {
			overrides, err := ss.loadCodexSkillEnabledOverrides(location)
			if err != nil {
				continue
			}
			for path, enabled := range overrides {
				codexEnabledByPath[path] = enabled
			}
		}
	}

	// 扫描用户级目录
	userPath, err := ss.getInstallPath(platform, skillLocationUser)
	if err == nil {
		userSkills := ss.scanSkillsDirectory(userPath, platform, skillLocationUser, codexEnabledByPath)
		allSkills = append(allSkills, userSkills...)
	}

	// 扫描项目级目录
	projectPath, err := ss.getInstallPath(platform, skillLocationProject)
	if err == nil {
		projectSkills := ss.scanSkillsDirectory(projectPath, platform, skillLocationProject, codexEnabledByPath)
		allSkills = append(allSkills, projectSkills...)
	}

	if platform == skillPlatformCodex {
		pluginSkills := ss.scanCodexPluginSkills(codexEnabledByPath)
		allSkills = append(allSkills, pluginSkills...)
	}

	// 按名称排序
	sort.SliceStable(allSkills, func(i, j int) bool {
		return strings.ToLower(allSkills[i].Name) < strings.ToLower(allSkills[j].Name)
	})

	return allSkills, nil
}

// scanSkillsDirectory 扫描目录中的技能
func (ss *SkillService) scanSkillsDirectory(dir, platform, location string, codexEnabledByPath map[string]bool) []Skill {
	var skills []Skill

	entries, err := os.ReadDir(dir)
	if err != nil {
		return skills
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillPath := filepath.Join(dir, entry.Name())
		skillMDPath := filepath.Join(skillPath, "SKILL.md")

		// 检查 SKILL.md 是否存在
		if _, err := os.Stat(skillMDPath); err != nil {
			continue
		}

		// 读取元数据
		meta, injectEnabled, err := ss.readSkillMetadataExtended(skillPath, platform)
		if err != nil {
			continue
		}

		name := strings.TrimSpace(meta.Name)
		if name == "" {
			name = entry.Name()
		}

		// 检查 LICENSE 文件
		licenseFile := ""
		for _, lf := range []string{"LICENSE", "LICENSE.txt", "LICENSE.md"} {
			if _, err := os.Stat(filepath.Join(skillPath, lf)); err == nil {
				licenseFile = lf
				break
			}
		}

		enabled := ss.resolveSkillEnabled(platform, skillPath, codexEnabledByPath, meta)

		skill := Skill{
			Key:             fmt.Sprintf("%s:%s:%s", platform, location, entry.Name()),
			Name:            name,
			Description:     strings.TrimSpace(meta.Description),
			Directory:       entry.Name(),
			Installed:       true,
			Enabled:         enabled,
			InjectEnabled:   injectEnabled,
			LicenseFile:     licenseFile,
			Platform:        platform,
			InstallLocation: location,
		}

		skills = append(skills, skill)
	}

	return skills
}

func (ss *SkillService) getCodexPluginCachePath() (string, error) {
	home, err := getUserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户目录失败: %w", err)
	}
	return filepath.Join(home, ".codex", "plugins", "cache"), nil
}

func (ss *SkillService) getCodexPluginSkillPath(key string) (string, error) {
	parts := strings.Split(strings.TrimSpace(key), ":")
	if len(parts) != 6 || parts[0] != skillPlatformCodex || parts[1] != skillLocationPlugin {
		return "", errors.New("plugin 技能 key 格式无效")
	}

	for _, part := range parts[2:] {
		if !isSafePathSegment(part) {
			return "", fmt.Errorf("plugin 技能 key 包含非法路径段: %s", part)
		}
	}

	cacheRoot, err := ss.getCodexPluginCachePath()
	if err != nil {
		return "", err
	}

	sourceName := parts[2]
	pluginName := parts[3]
	version := parts[4]
	skillName := parts[5]
	versionPath := filepath.Join(cacheRoot, sourceName, pluginName, version)

	candidates := []string{
		filepath.Join(versionPath, "skills", skillName),
		filepath.Join(versionPath, ".codex", "skills", skillName),
	}
	for _, candidate := range candidates {
		if !isPathInsideRoot(cacheRoot, candidate) {
			continue
		}
		if _, err := os.Stat(filepath.Join(candidate, "SKILL.md")); err == nil {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("未找到 plugin 技能: %s", key)
}

func isSafePathSegment(segment string) bool {
	segment = strings.TrimSpace(segment)
	if segment == "" || segment == "." || segment == ".." {
		return false
	}
	return !strings.ContainsAny(segment, `/\`)
}

func isPathInsideRoot(root, target string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return false
	}
	return rel == "." || (rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func (ss *SkillService) scanCodexPluginSkills(codexEnabledByPath map[string]bool) []Skill {
	cacheRoot, err := ss.getCodexPluginCachePath()
	if err != nil {
		return nil
	}
	return ss.scanCodexPluginSkillsFromCache(cacheRoot, codexEnabledByPath)
}

func (ss *SkillService) scanCodexPluginSkillsFromCache(cacheRoot string, codexEnabledByPath map[string]bool) []Skill {
	var skills []Skill

	sourceEntries, err := os.ReadDir(cacheRoot)
	if err != nil {
		return skills
	}

	for _, sourceEntry := range sourceEntries {
		if !sourceEntry.IsDir() {
			continue
		}
		sourceName := sourceEntry.Name()
		sourcePath := filepath.Join(cacheRoot, sourceName)

		pluginEntries, err := os.ReadDir(sourcePath)
		if err != nil {
			continue
		}
		for _, pluginEntry := range pluginEntries {
			if !pluginEntry.IsDir() {
				continue
			}
			pluginName := pluginEntry.Name()
			pluginPath := filepath.Join(sourcePath, pluginName)

			versionEntries, err := os.ReadDir(pluginPath)
			if err != nil {
				continue
			}
			versionEntry, ok := selectCodexPluginVersionEntry(versionEntries)
			if !ok {
				continue
			}
			version := versionEntry.Name()
			versionPath := filepath.Join(pluginPath, version)

			// cache 会同时保留旧版本，并可能额外创建 latest junction。它不是“全部已安装版本”的事实源；
			// 每个 plugin 只展示最高版本，避免同一技能在 UI 中重复出现并让开关写到过期缓存。
			for _, skillsRoot := range []string{
				filepath.Join(versionPath, "skills"),
				filepath.Join(versionPath, ".codex", "skills"),
			} {
				skills = append(skills, ss.scanCodexPluginSkillsRoot(
					skillsRoot,
					sourceName,
					pluginName,
					version,
					codexEnabledByPath,
				)...)
			}
		}
	}

	return skills
}

func selectCodexPluginVersionEntry(entries []os.DirEntry) (os.DirEntry, bool) {
	var selected os.DirEntry
	for _, entry := range entries {
		if !entry.IsDir() || strings.EqualFold(entry.Name(), "latest") {
			continue
		}
		if selected == nil || compareCodexPluginVersions(entry.Name(), selected.Name()) > 0 {
			selected = entry
		}
	}
	return selected, selected != nil
}

func compareCodexPluginVersions(left string, right string) int {
	leftSemver := "v" + strings.TrimPrefix(strings.TrimSpace(left), "v")
	rightSemver := "v" + strings.TrimPrefix(strings.TrimSpace(right), "v")
	if semver.IsValid(leftSemver) && semver.IsValid(rightSemver) {
		return semver.Compare(leftSemver, rightSemver)
	}
	return strings.Compare(strings.ToLower(left), strings.ToLower(right))
}

func (ss *SkillService) scanCodexPluginSkillsRoot(
	skillsRoot string,
	sourceName string,
	pluginName string,
	version string,
	codexEnabledByPath map[string]bool,
) []Skill {
	var skills []Skill

	entries, err := os.ReadDir(skillsRoot)
	if err != nil {
		return skills
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillPath := filepath.Join(skillsRoot, entry.Name())
		if _, err := os.Stat(filepath.Join(skillPath, "SKILL.md")); err != nil {
			continue
		}

		meta, injectEnabled, err := ss.readSkillMetadataExtended(skillPath, skillPlatformCodex)
		if err != nil {
			continue
		}

		name := strings.TrimSpace(meta.Name)
		if name == "" {
			name = entry.Name()
		}

		licenseFile := ""
		for _, lf := range []string{"LICENSE", "LICENSE.txt", "LICENSE.md"} {
			if _, err := os.Stat(filepath.Join(skillPath, lf)); err == nil {
				licenseFile = lf
				break
			}
		}

		skills = append(skills, Skill{
			Key:             fmt.Sprintf("codex:plugin:%s:%s:%s:%s", sourceName, pluginName, version, entry.Name()),
			Name:            name,
			Description:     strings.TrimSpace(meta.Description),
			Directory:       entry.Name(),
			Installed:       true,
			Enabled:         ss.resolveSkillEnabled(skillPlatformCodex, skillPath, codexEnabledByPath, meta),
			InjectEnabled:   injectEnabled,
			LicenseFile:     licenseFile,
			Platform:        skillPlatformCodex,
			InstallLocation: skillLocationPlugin,
			Readonly:        true,
			PluginSource:    sourceName,
			PluginName:      pluginName,
			PluginVersion:   version,
		})
	}

	return skills
}

// readSkillMetadataExtended 读取技能元数据（包括 enabled/inject 状态）
func (ss *SkillService) readSkillMetadataExtended(dir, platform string) (skillMetadataExtended, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return skillMetadataExtended{}, false, err
	}

	meta, err := parseSkillMetadataExtended(string(data))
	if err != nil {
		return skillMetadataExtended{}, false, err
	}

	injectEnabled := true
	if platform == skillPlatformCodex {
		if injected, ok, err := ss.readCodexInjectEnabled(dir); err == nil && ok {
			injectEnabled = injected
		} else if err != nil {
			return skillMetadataExtended{}, false, err
		}
	} else if meta.Policy.AllowImplicitInvocation != nil {
		injectEnabled = *meta.Policy.AllowImplicitInvocation
	}

	return meta, injectEnabled, nil
}

// parseSkillMetadataExtended 解析扩展元数据
func parseSkillMetadataExtended(content string) (skillMetadataExtended, error) {
	var meta skillMetadataExtended
	content = strings.TrimLeft(content, "\ufeff")

	// 使用 splitFrontMatter 替代 strings.SplitN，避免 YAML 值中的 --- 被误判
	_, fmLines, _, err := splitFrontMatter(content)
	if err != nil {
		return meta, errors.New("SKILL.md 缺少 front matter")
	}

	frontMatter := strings.Join(fmLines, "\n")
	if err := yaml.Unmarshal([]byte(frontMatter), &meta); err != nil {
		return meta, err
	}
	return meta, nil
}

func (ss *SkillService) readCodexInjectEnabled(dir string) (bool, bool, error) {
	metadataPath := filepath.Join(dir, "agents", "openai.yaml")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, false, nil
		}
		return false, false, fmt.Errorf("读取 openai.yaml 失败: %w", err)
	}

	var meta codexSkillMetadataFile
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return false, false, fmt.Errorf("解析 openai.yaml 失败: %w", err)
	}
	if meta.Policy.AllowImplicitInvocation == nil {
		return true, false, nil
	}
	return *meta.Policy.AllowImplicitInvocation, true, nil
}

func (ss *SkillService) resolveSkillEnabled(
	platform string,
	skillPath string,
	codexEnabledByPath map[string]bool,
	meta skillMetadataExtended,
) bool {
	if platform == skillPlatformCodex {
		normalized := normalizePathKey(skillPath)
		if enabled, ok := codexEnabledByPath[normalized]; ok {
			return enabled
		}
		return true
	}
	return !meta.DisableModelInvocation
}

func normalizePathKey(path string) string {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" {
		return ""
	}
	return strings.ToLower(cleaned)
}

func (ss *SkillService) loadCodexSkillEnabledOverrides(location string) (map[string]bool, error) {
	configPath, err := ss.getCodexConfigPath(location)
	if err != nil {
		return nil, err
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]bool{}, nil
		}
		return nil, err
	}

	var raw map[string]any
	if len(content) > 0 {
		if err := toml.Unmarshal(content, &raw); err != nil {
			return nil, fmt.Errorf("解析 Codex config.toml 失败: %w", err)
		}
	}
	if raw == nil {
		return map[string]bool{}, nil
	}

	skillsRaw, ok := raw["skills"].(map[string]any)
	if !ok || skillsRaw == nil {
		return map[string]bool{}, nil
	}

	configsRaw, ok := skillsRaw["config"]
	if !ok {
		return map[string]bool{}, nil
	}

	items := normalizeTomlArray(configsRaw)
	result := make(map[string]bool, len(items))
	for _, item := range items {
		entryMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		pathValue, ok := entryMap["path"].(string)
		if !ok || strings.TrimSpace(pathValue) == "" {
			continue
		}
		enabledValue, ok := entryMap["enabled"].(bool)
		if !ok {
			continue
		}
		result[normalizePathKey(pathValue)] = enabledValue
	}
	return result, nil
}

func (ss *SkillService) getCodexConfigPath(location string) (string, error) {
	var basePath string
	switch location {
	case skillLocationProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("获取工作目录失败: %w", err)
		}
		basePath = cwd
	default:
		home, err := getUserHomeDir()
		if err != nil {
			return "", fmt.Errorf("获取用户目录失败: %w", err)
		}
		basePath = home
	}
	return filepath.Join(basePath, codexSettingsDir, codexConfigFileName), nil
}

func (ss *SkillService) ToggleSkillEnabled(directory, platform, location string, enabled bool) error {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return errors.New("skill directory 不能为空")
	}

	if platform == "" {
		platform = skillPlatformClaude
	}
	if location == "" {
		location = skillLocationUser
	}
	if location == skillLocationPlugin {
		return errors.New("plugin 技能为只读缓存，不支持切换启用状态")
	}

	if platform != skillPlatformCodex {
		return ss.toggleSkillLegacy(directory, platform, location, enabled)
	}

	skillPath, err := ss.getInstalledSkillPath(directory, platform, location)
	if err != nil {
		return err
	}

	configPath, err := ss.getCodexConfigPath(location)
	if err != nil {
		return err
	}

	raw := make(map[string]any)
	if content, readErr := os.ReadFile(configPath); readErr == nil {
		if err := toml.Unmarshal(content, &raw); err != nil {
			return fmt.Errorf("config.toml 解析失败，请检查文件格式: %w", err)
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if raw == nil {
		raw = make(map[string]any)
	}

	ss.upsertCodexSkillConfig(raw, skillPath, enabled)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}

	css := NewCodexSettingsService("")
	return css.writeConfigToml(configPath, raw)
}

func (ss *SkillService) ToggleSkillInjection(directory, platform, location string, injectEnabled bool) error {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return errors.New("skill directory 不能为空")
	}

	if platform == "" {
		platform = skillPlatformClaude
	}
	if location == "" {
		location = skillLocationUser
	}

	if platform != skillPlatformCodex {
		return errors.New("当前仅 Codex 平台支持注入开关")
	}

	var skillPath string
	if location == skillLocationPlugin {
		// Plugin skill 可能同名，必须使用完整 key 定位缓存中的具体技能目录。
		// 这里仅写 agents/openai.yaml 注入策略，不修改 SKILL.md，也不允许卸载缓存目录。
		path, err := ss.getCodexPluginSkillPath(directory)
		if err != nil {
			return err
		}
		skillPath = path
	} else {
		installPath, err := ss.getInstallPath(platform, location)
		if err != nil {
			return err
		}
		skillPath = filepath.Join(installPath, directory)
	}

	metadataPath := filepath.Join(skillPath, "agents", "openai.yaml")

	data, err := os.ReadFile(metadataPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			content := buildCodexInjectMetadata(injectEnabled)
			return AtomicWriteBytes(metadataPath, []byte(content))
		}
		return fmt.Errorf("读取 openai.yaml 失败: %w", err)
	}

	newContent, changed, err := patchNestedYamlBool(
		string(data),
		[]string{"policy", "allow_implicit_invocation"},
		injectEnabled,
	)
	if err != nil {
		return fmt.Errorf("修改 openai.yaml 失败: %w", err)
	}
	if !changed {
		return nil
	}

	return AtomicWriteBytes(metadataPath, []byte(newContent))
}

func buildCodexInjectMetadata(injectEnabled bool) string {
	desired := "false"
	if injectEnabled {
		desired = "true"
	}
	return "policy:\n  allow_implicit_invocation: " + desired + "\n"
}

func (ss *SkillService) ToggleSkill(directory, platform, location string, enabled bool) error {
	return ss.ToggleSkillEnabled(directory, platform, location, enabled)
}

func (ss *SkillService) toggleSkillLegacy(directory, platform, location string, enabled bool) error {
	installPath, err := ss.getInstallPath(platform, location)
	if err != nil {
		return err
	}

	skillMDPath := filepath.Join(installPath, directory, "SKILL.md")
	data, err := os.ReadFile(skillMDPath)
	if err != nil {
		return fmt.Errorf("读取 SKILL.md 失败: %w", err)
	}

	newContent, changed, err := patchSkillFrontMatterBool(
		string(data),
		"disable-model-invocation",
		!enabled,
	)
	if err != nil {
		return fmt.Errorf("修改 SKILL.md 失败: %w", err)
	}
	if !changed {
		return nil
	}

	return AtomicWriteBytes(skillMDPath, []byte(newContent))
}

func (ss *SkillService) getInstalledSkillPath(directory, platform, location string) (string, error) {
	installPath, err := ss.getInstallPath(platform, location)
	if err != nil {
		return "", err
	}
	skillPath := filepath.Join(installPath, directory)
	if _, err := os.Stat(filepath.Join(skillPath, "SKILL.md")); err != nil {
		return "", fmt.Errorf("未找到技能 %s: %w", directory, err)
	}
	return skillPath, nil
}

func (ss *SkillService) upsertCodexSkillConfig(raw map[string]any, skillPath string, enabled bool) {
	skillsTable, ok := raw["skills"].(map[string]any)
	if !ok || skillsTable == nil {
		skillsTable = make(map[string]any)
		raw["skills"] = skillsTable
	}

	configList := normalizeTomlArray(skillsTable["config"])

	normalizedPath := normalizePathKey(skillPath)
	filtered := make([]any, 0, len(configList))
	for _, item := range configList {
		entry, ok := item.(map[string]any)
		if !ok {
			filtered = append(filtered, item)
			continue
		}
		pathValue, _ := entry["path"].(string)
		if normalizePathKey(pathValue) == normalizedPath {
			continue
		}
		filtered = append(filtered, item)
	}

	if !enabled {
		filtered = append(filtered, map[string]any{
			"path":    skillPath,
			"enabled": false,
		})
	}

	if len(filtered) == 0 {
		delete(skillsTable, "config")
	} else {
		skillsTable["config"] = filtered
	}
	if len(skillsTable) == 0 {
		delete(raw, "skills")
	}
}

func normalizeTomlArray(value any) []any {
	switch typed := value.(type) {
	case nil:
		return []any{}
	case []any:
		return append([]any(nil), typed...)
	case []map[string]any:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			result = append(result, item)
		}
		return result
	default:
		return []any{}
	}
}

// ListSkills aggregates skills from configured repositories and the local install directory.
func (ss *SkillService) ListSkills() ([]Skill, error) {
	store, err := ss.loadStore()
	if err != nil {
		return nil, err
	}

	skillMap := make(map[string]Skill)
	for _, repo := range store.Repos {
		if !repo.Enabled {
			continue
		}
		repoDir, branch, cleanup, err := ss.prepareRepoSnapshot(repo)
		if err != nil {
			log.Printf("skill repo fetch failed for %s/%s: %v", repo.Owner, repo.Name, err)
			continue
		}
		entries, err := os.ReadDir(repoDir)
		if err != nil {
			cleanup()
			log.Printf("skill repo read failed for %s/%s: %v", repo.Owner, repo.Name, err)
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dirKey := normalizeDirectoryKey(entry.Name())
			if _, exists := skillMap[dirKey]; exists {
				continue
			}
			skillPath := filepath.Join(repoDir, entry.Name())
			meta, err := readSkillMetadata(skillPath)
			if err != nil {
				continue
			}
			name := strings.TrimSpace(meta.Name)
			if name == "" {
				name = entry.Name()
			}
			key := buildSkillKey(repo.Owner, repo.Name, entry.Name())
			skillMap[dirKey] = Skill{
				Key:         key,
				Name:        name,
				Description: strings.TrimSpace(meta.Description),
				Directory:   entry.Name(),
				ReadmeURL:   buildRepoURL(repo, branch, entry.Name()),
				Installed:   ss.isInstalled(entry.Name()),
				RepoOwner:   repo.Owner,
				RepoName:    repo.Name,
				RepoBranch:  branch,
			}
		}
		cleanup()
	}

	ss.mergeLocalSkills(skillMap)
	skills := make([]Skill, 0, len(skillMap))
	for _, skill := range skillMap {
		skills = append(skills, skill)
	}
	sort.SliceStable(skills, func(i, j int) bool {
		li := strings.ToLower(skills[i].Name)
		lj := strings.ToLower(skills[j].Name)
		if li == lj {
			return strings.ToLower(skills[i].Directory) < strings.ToLower(skills[j].Directory)
		}
		return li < lj
	})
	return skills, nil
}

// InstallSkill installs a skill directory from the configured repositories.
// 支持 platform 和 location 参数，用于指定安装的平台和位置
func (ss *SkillService) InstallSkill(req installRequest) error {
	req.Directory = strings.TrimSpace(req.Directory)
	if req.Directory == "" {
		return errors.New("skill directory 不能为空")
	}

	// 设置默认值
	if req.Platform == "" {
		req.Platform = skillPlatformClaude
	}
	if req.Location == "" {
		req.Location = skillLocationUser
	}
	if req.Location == skillLocationPlugin {
		return errors.New("plugin 技能为只读缓存，不支持安装到该位置")
	}

	store, err := ss.loadStore()
	if err != nil {
		return err
	}
	repos := ss.resolveReposForInstall(req, store.Repos)
	if len(repos) == 0 {
		return errors.New("未找到可用的技能仓库")
	}

	var lastErr error
	for _, repo := range repos {
		repoDir, _, cleanup, err := ss.prepareRepoSnapshot(repo)
		if err != nil {
			lastErr = err
			continue
		}
		skillPath := filepath.Join(repoDir, req.Directory)
		info, err := os.Stat(skillPath)
		if err != nil || !info.IsDir() {
			cleanup()
			lastErr = fmt.Errorf("仓库 %s/%s 中未找到 %s", repo.Owner, repo.Name, req.Directory)
			continue
		}
		if err := ss.installFromPathEx(req.Directory, skillPath, req.Platform, req.Location); err != nil {
			cleanup()
			lastErr = err
			continue
		}
		cleanup()
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("skill %s 未找到", req.Directory)
	}
	return lastErr
}

func (ss *SkillService) installFromPath(directory, source string) error {
	return ss.installFromPathEx(directory, source, skillPlatformClaude, skillLocationUser)
}

// installFromPathEx 安装技能到指定平台和位置
func (ss *SkillService) installFromPathEx(directory, source, platform, location string) error {
	if _, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil {
		return fmt.Errorf("%s 缺少 SKILL.md", directory)
	}

	// 获取安装路径
	installPath, err := ss.getInstallPath(platform, location)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(installPath, 0o755); err != nil {
		return err
	}
	target := filepath.Join(installPath, directory)
	if err := os.RemoveAll(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := copyDirectory(source, target); err != nil {
		return err
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	store, err := ss.loadStoreLocked()
	if err != nil {
		return err
	}
	if store.Skills == nil {
		store.Skills = make(map[string]skillState)
	}
	store.Skills[directory] = skillState{Installed: true, InstalledAt: time.Now()}
	return ss.saveStoreLocked(store)
}

func (ss *SkillService) UninstallSkill(directory string) error {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return errors.New("skill directory 不能为空")
	}
	if err := ss.requireUserStorage(); err != nil {
		return err
	}
	target := filepath.Join(ss.installDir, directory)
	if err := os.RemoveAll(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	store, err := ss.loadStoreLocked()
	if err != nil {
		return err
	}
	if store.Skills == nil {
		store.Skills = make(map[string]skillState)
	}
	delete(store.Skills, directory)
	return ss.saveStoreLocked(store)
}

// UninstallSkillEx 卸载技能（支持多平台多位置）
func (ss *SkillService) UninstallSkillEx(directory, platform, location string) error {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return errors.New("skill directory 不能为空")
	}

	// 默认值
	if platform == "" {
		platform = skillPlatformClaude
	}
	if location == "" {
		location = skillLocationUser
	}
	if location == skillLocationPlugin {
		return errors.New("plugin 技能为只读缓存，不支持卸载")
	}

	installPath, err := ss.getInstallPath(platform, location)
	if err != nil {
		return err
	}

	target := filepath.Join(installPath, directory)
	if err := os.RemoveAll(target); err != nil && !os.IsNotExist(err) {
		return err
	}

	// 更新 store
	ss.mu.Lock()
	defer ss.mu.Unlock()
	store, err := ss.loadStoreLocked()
	if err != nil {
		return err
	}
	if store.Skills == nil {
		store.Skills = make(map[string]skillState)
	}
	delete(store.Skills, directory)
	return ss.saveStoreLocked(store)
}

// splitFrontMatter 使用行首匹配 ^---\s*$ 来分割 front matter
// 返回 (prefix, frontMatterLines, body, error)
// prefix: 开始 --- 之前的行
// frontMatterLines: front matter 内容（不含边界行）
// body: 结束 --- 之后的所有内容
func splitFrontMatter(content string) (prefix string, fmLines []string, body string, err error) {
	// 统一行尾为 \n 进行处理
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")

	startIdx := -1
	endIdx := -1

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			if startIdx == -1 {
				startIdx = i
			} else {
				endIdx = i
				break
			}
		}
	}

	if startIdx == -1 || endIdx == -1 {
		return "", nil, "", errors.New("无法解析 front matter：未找到有效的 --- 边界")
	}

	// prefix: lines[0:startIdx]
	if startIdx > 0 {
		prefix = strings.Join(lines[:startIdx], "\n") + "\n"
	}

	// fmLines: lines[startIdx+1:endIdx]
	fmLines = lines[startIdx+1 : endIdx]

	// body: lines[endIdx+1:]
	if endIdx+1 < len(lines) {
		body = strings.Join(lines[endIdx+1:], "\n")
	}

	return prefix, fmLines, body, nil
}

// patchSkillFrontMatterBool 最小化修改 SKILL.md 的 front matter 中的布尔字段
// 保留原有格式、注释和字段顺序
func patchSkillFrontMatterBool(markdown, key string, desired bool) (string, bool, error) {
	// 1. 保留 BOM
	hasBOM := false
	if strings.HasPrefix(markdown, "\ufeff") {
		hasBOM = true
		markdown = strings.TrimPrefix(markdown, "\ufeff")
	}

	// 2. 检测行尾风格
	hasCRLF := strings.Contains(markdown, "\r\n")

	// 3. 分割 front matter（使用行首匹配，避免内容中的 --- 被误判）
	prefix, lines, body, err := splitFrontMatter(markdown)
	if err != nil {
		return "", false, err
	}

	// 4. 按行处理 front matter
	keyFound := false
	modified := false
	desiredStr := "false"
	if desired {
		desiredStr = "true"
	}

	for i, line := range lines {
		// 移除可能的 \r
		cleanLine := strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(cleanLine)

		// 检查是否匹配目标 key
		if strings.HasPrefix(trimmed, key+":") {
			keyFound = true

			// 提取当前值
			colonIdx := strings.Index(trimmed, ":")
			valuePart := strings.TrimSpace(trimmed[colonIdx+1:])

			// 处理可能的行内注释
			comment := ""
			hashIdx := strings.Index(valuePart, "#")
			if hashIdx != -1 {
				comment = valuePart[hashIdx:]
				valuePart = strings.TrimSpace(valuePart[:hashIdx])
			}

			// 检查是否需要修改
			currentBool := strings.ToLower(valuePart) == "true"
			if currentBool == desired {
				continue // 值已经正确，无需修改
			}

			// 构建新行（保留原有缩进）
			indent := ""
			for _, ch := range cleanLine {
				if ch == ' ' || ch == '\t' {
					indent += string(ch)
				} else {
					break
				}
			}

			newLine := indent + key + ": " + desiredStr
			if comment != "" {
				newLine += " " + comment
			}

			lines[i] = newLine
			modified = true
		}
	}

	// 5. 如果 key 不存在，在 front matter 末尾插入
	if !keyFound {
		insertLine := key + ": " + desiredStr
		// 在最后一行（通常是空行）之前插入
		insertIdx := len(lines) - 1
		for insertIdx > 0 && strings.TrimSpace(lines[insertIdx]) == "" {
			insertIdx--
		}
		insertIdx++

		newLines := make([]string, 0, len(lines)+1)
		newLines = append(newLines, lines[:insertIdx]...)
		newLines = append(newLines, insertLine)
		newLines = append(newLines, lines[insertIdx:]...)
		lines = newLines
		modified = true
	}

	// 6. 重建文档
	newFrontMatter := strings.Join(lines, "\n")
	result := prefix + "---\n" + newFrontMatter + "\n---\n" + body

	// 7. 恢复 CRLF（如果原文使用）
	if hasCRLF {
		// 先统一为 LF，再替换为 CRLF
		result = strings.ReplaceAll(result, "\r\n", "\n")
		result = strings.ReplaceAll(result, "\n", "\r\n")
	}

	// 8. 恢复 BOM
	if hasBOM {
		result = "\ufeff" + result
	}

	return result, modified, nil
}

func patchNestedYamlBool(content string, keyPath []string, desired bool) (string, bool, error) {
	if len(keyPath) != 2 {
		return "", false, errors.New("仅支持两级嵌套 key")
	}

	hasBOM := false
	if strings.HasPrefix(content, "\ufeff") {
		hasBOM = true
		content = strings.TrimPrefix(content, "\ufeff")
	}

	hasCRLF := strings.Contains(content, "\r\n")
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")

	parentKey := keyPath[0]
	childKey := keyPath[1]
	desiredStr := "false"
	if desired {
		desiredStr = "true"
	}

	parentIndex := -1
	parentIndent := 0
	childFound := false
	modified := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		indentLen := len(line) - len(strings.TrimLeft(line, " \t"))
		if parentIndex == -1 {
			if indentLen == 0 && strings.HasPrefix(trimmed, parentKey+":") {
				parentIndex = i
				parentIndent = indentLen
			}
			continue
		}

		if indentLen <= parentIndent {
			break
		}

		expectedPrefix := childKey + ":"
		if indentLen == parentIndent+2 && strings.HasPrefix(strings.TrimSpace(line), expectedPrefix) {
			childFound = true
			currentTrimmed := strings.TrimSpace(line)
			colonIdx := strings.Index(currentTrimmed, ":")
			valuePart := strings.TrimSpace(currentTrimmed[colonIdx+1:])
			comment := ""
			hashIdx := strings.Index(valuePart, "#")
			if hashIdx != -1 {
				comment = valuePart[hashIdx:]
				valuePart = strings.TrimSpace(valuePart[:hashIdx])
			}
			currentBool := strings.EqualFold(valuePart, "true")
			if currentBool == desired {
				break
			}

			newLine := strings.Repeat(" ", parentIndent+2) + childKey + ": " + desiredStr
			if comment != "" {
				newLine += " " + comment
			}
			lines[i] = newLine
			modified = true
			break
		}
	}

	if parentIndex == -1 {
		insertBlock := []string{
			parentKey + ":",
			"  " + childKey + ": " + desiredStr,
		}
		insertIdx := len(lines)
		for insertIdx > 0 && strings.TrimSpace(lines[insertIdx-1]) == "" {
			insertIdx--
		}
		newLines := make([]string, 0, len(lines)+len(insertBlock)+1)
		newLines = append(newLines, lines[:insertIdx]...)
		if insertIdx > 0 && strings.TrimSpace(lines[insertIdx-1]) != "" {
			newLines = append(newLines, "")
		}
		newLines = append(newLines, insertBlock...)
		newLines = append(newLines, lines[insertIdx:]...)
		lines = newLines
		modified = true
	} else if !childFound {
		insertIdx := parentIndex + 1
		for insertIdx < len(lines) {
			trimmed := strings.TrimSpace(lines[insertIdx])
			if trimmed == "" {
				insertIdx++
				continue
			}
			indentLen := len(lines[insertIdx]) - len(strings.TrimLeft(lines[insertIdx], " \t"))
			if indentLen <= parentIndent {
				break
			}
			insertIdx++
		}

		newLine := strings.Repeat(" ", parentIndent+2) + childKey + ": " + desiredStr
		newLines := make([]string, 0, len(lines)+1)
		newLines = append(newLines, lines[:insertIdx]...)
		newLines = append(newLines, newLine)
		newLines = append(newLines, lines[insertIdx:]...)
		lines = newLines
		modified = true
	}

	result := strings.Join(lines, "\n")
	if !modified {
		return restoreTextFormatting(result, hasCRLF, hasBOM), false, nil
	}

	return restoreTextFormatting(result, hasCRLF, hasBOM), true, nil
}

func restoreTextFormatting(result string, hasCRLF, hasBOM bool) string {
	if hasCRLF {
		result = strings.ReplaceAll(result, "\r\n", "\n")
		result = strings.ReplaceAll(result, "\n", "\r\n")
	}
	if hasBOM {
		result = "\ufeff" + result
	}
	return result
}

// GetSkillContent 获取技能的 SKILL.md 内容
func (ss *SkillService) GetSkillContent(directory, platform, location string) (string, error) {
	if directory == "" {
		return "", errors.New("skill directory 不能为空")
	}

	if platform == skillPlatformCodex && location == skillLocationPlugin {
		// Plugin 技能可能与用户技能同名，必须用完整 key 定位来源。
		// 这里仅解析服务端生成的 codex:plugin:<source>:<plugin>:<version>:<skill>，避免任意路径读取。
		skillPath, err := ss.getCodexPluginSkillPath(directory)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(filepath.Join(skillPath, "SKILL.md"))
		if err != nil {
			return "", fmt.Errorf("读取 plugin SKILL.md 失败: %w", err)
		}
		return string(data), nil
	}

	installPath, err := ss.getInstallPath(platform, location)
	if err != nil {
		return "", err
	}

	skillMDPath := filepath.Join(installPath, directory, "SKILL.md")
	data, err := os.ReadFile(skillMDPath)
	if err != nil {
		return "", fmt.Errorf("读取 SKILL.md 失败: %w", err)
	}

	return string(data), nil
}

// SaveSkillContent 保存技能的 SKILL.md 内容
func (ss *SkillService) SaveSkillContent(directory, platform, location, content string) error {
	if directory == "" {
		return errors.New("skill directory 不能为空")
	}
	if location == skillLocationPlugin {
		return errors.New("plugin 技能为只读缓存，不支持保存")
	}

	installPath, err := ss.getInstallPath(platform, location)
	if err != nil {
		return err
	}

	skillMDPath := filepath.Join(installPath, directory, "SKILL.md")

	// 原子写入
	return AtomicWriteBytes(skillMDPath, []byte(content))
}

// OpenSkillFolder 打开技能目录
func (ss *SkillService) OpenSkillFolder(platform, location string) error {
	installPath, err := ss.getInstallPath(platform, location)
	if err != nil {
		return err
	}

	// 确保目录存在
	if err := os.MkdirAll(installPath, 0o755); err != nil {
		return err
	}

	return OpenInExplorer(installPath)
}

// Repository management ----------------------------------------------------

func (ss *SkillService) ListRepos() ([]skillRepoConfig, error) {
	store, err := ss.loadStore()
	if err != nil {
		return nil, err
	}
	return cloneRepoConfigs(store.Repos), nil
}

func (ss *SkillService) AddRepo(repo skillRepoConfig) ([]skillRepoConfig, error) {
	repo = normalizeRepoConfig(repo)
	if err := validateRepoConfig(repo); err != nil {
		return nil, err
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	store, err := ss.loadStoreLocked()
	if err != nil {
		return nil, err
	}
	replaced := false
	for i := range store.Repos {
		if equalRepo(store.Repos[i], repo) {
			store.Repos[i] = repo
			replaced = true
			break
		}
	}
	if !replaced {
		store.Repos = append(store.Repos, repo)
	}
	if err := ss.saveStoreLocked(store); err != nil {
		return nil, err
	}
	return cloneRepoConfigs(store.Repos), nil
}

func (ss *SkillService) RemoveRepo(owner, name string) ([]skillRepoConfig, error) {
	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)
	if owner == "" || name == "" {
		return nil, errors.New("owner/name 不能为空")
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	store, err := ss.loadStoreLocked()
	if err != nil {
		return nil, err
	}
	filtered := make([]skillRepoConfig, 0, len(store.Repos))
	for _, repo := range store.Repos {
		if strings.EqualFold(repo.Owner, owner) && strings.EqualFold(repo.Name, name) {
			continue
		}
		filtered = append(filtered, repo)
	}
	if len(filtered) == 0 {
		filtered = cloneDefaultRepos()
	}
	store.Repos = filtered
	if err := ss.saveStoreLocked(store); err != nil {
		return nil, err
	}
	return cloneRepoConfigs(store.Repos), nil
}

// Internal helpers ---------------------------------------------------------

func (ss *SkillService) loadStore() (skillStore, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.loadStoreLocked()
}

func (ss *SkillService) loadStoreLocked() (skillStore, error) {
	if err := ss.requireUserStorage(); err != nil {
		return skillStore{Skills: make(map[string]skillState)}, err
	}
	data, err := os.ReadFile(ss.storePath)
	if err != nil {
		if os.IsNotExist(err) {
			store := skillStore{Skills: make(map[string]skillState)}
			store.ensureRepos()
			return store, nil
		}
		return skillStore{Skills: make(map[string]skillState)}, err
	}
	store := skillStore{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &store); err != nil {
			return skillStore{Skills: make(map[string]skillState)}, err
		}
	}
	if store.Skills == nil {
		store.Skills = make(map[string]skillState)
	}
	store.ensureRepos()
	return store, nil
}

func (store *skillStore) ensureRepos() {
	if len(store.Repos) == 0 {
		store.Repos = cloneDefaultRepos()
	}
	for i := range store.Repos {
		store.Repos[i] = normalizeRepoConfig(store.Repos[i])
		if !store.Repos[i].Enabled {
			store.Repos[i].Enabled = true
		}
	}
}

func cloneDefaultRepos() []skillRepoConfig {
	repos := make([]skillRepoConfig, len(defaultSkillRepos))
	copy(repos, defaultSkillRepos)
	return repos
}

func cloneRepoConfigs(repos []skillRepoConfig) []skillRepoConfig {
	copyRepos := make([]skillRepoConfig, len(repos))
	copy(copyRepos, repos)
	return copyRepos
}

func normalizeRepoConfig(repo skillRepoConfig) skillRepoConfig {
	repo.Owner = strings.TrimSpace(repo.Owner)
	repo.Name = strings.TrimSpace(repo.Name)
	repo.Branch = strings.TrimSpace(repo.Branch)
	if repo.Branch == "" {
		repo.Branch = "main"
	}
	if !repo.Enabled {
		repo.Enabled = true
	}
	return repo
}

func validateRepoConfig(repo skillRepoConfig) error {
	if repo.Owner == "" || repo.Name == "" {
		return errors.New("owner/name 不能为空")
	}
	return nil
}

func equalRepo(a, b skillRepoConfig) bool {
	return strings.EqualFold(a.Owner, b.Owner) && strings.EqualFold(a.Name, b.Name)
}

func (ss *SkillService) saveStoreLocked(store skillStore) error {
	if err := ss.requireUserStorage(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(ss.storePath), 0o755); err != nil {
		return err
	}
	store.ensureRepos()
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	tmp := ss.storePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, ss.storePath)
}

func (ss *SkillService) prepareRepoSnapshot(repo skillRepoConfig) (string, string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "skill-repo-")
	if err != nil {
		return "", "", nil, err
	}
	cleanup := func() {
		_ = os.RemoveAll(tmpDir)
	}
	archivePath := filepath.Join(tmpDir, "repo.zip")
	branches := buildBranchCandidates(repo.Branch)
	var lastErr error
	for _, branch := range branches {
		archiveURL := fmt.Sprintf("https://github.com/%s/%s/archive/refs/heads/%s.zip", repo.Owner, repo.Name, branch)
		if err := ss.downloadFile(archiveURL, archivePath); err != nil {
			lastErr = err
			continue
		}
		rootDir, err := unzipArchive(archivePath, tmpDir)
		if err != nil {
			lastErr = err
			continue
		}
		return rootDir, branch, cleanup, nil
	}
	cleanup()
	if lastErr == nil {
		lastErr = fmt.Errorf("无法下载仓库 %s/%s", repo.Owner, repo.Name)
	}
	return "", "", nil, lastErr
}

func buildBranchCandidates(preferred string) []string {
	set := make(map[string]struct{})
	ordered := make([]string, 0, len(defaultRepoBranches)+1)
	if preferred != "" {
		set[strings.ToLower(preferred)] = struct{}{}
		ordered = append(ordered, preferred)
	}
	for _, branch := range defaultRepoBranches {
		key := strings.ToLower(branch)
		if _, ok := set[key]; ok {
			continue
		}
		set[key] = struct{}{}
		ordered = append(ordered, branch)
	}
	return ordered
}

func (ss *SkillService) downloadFile(rawURL, dest string) error {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ai-code-studio")
	resp, err := ss.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败: %s", resp.Status)
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return err
	}
	return nil
}

func unzipArchive(zipPath, dest string) (string, error) {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	var root string
	for _, file := range reader.File {
		name := file.Name
		if name == "" {
			continue
		}
		if root == "" {
			root = strings.Split(name, "/")[0]
		}
		targetPath := filepath.Join(dest, name)
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return "", err
		}
		src, err := file.Open()
		if err != nil {
			return "", err
		}
		dst, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, file.Mode())
		if err != nil {
			src.Close()
			return "", err
		}
		if _, err := io.Copy(dst, src); err != nil {
			src.Close()
			dst.Close()
			return "", err
		}
		src.Close()
		dst.Close()
	}
	if root == "" {
		return "", errors.New("压缩包内容为空")
	}
	return filepath.Join(dest, root), nil
}

func (ss *SkillService) mergeLocalSkills(skills map[string]Skill) {
	if ss.requireUserStorage() != nil {
		return
	}
	entries, err := os.ReadDir(ss.installDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := entry.Name()
		dirKey := normalizeDirectoryKey(dir)
		if existing, ok := skills[dirKey]; ok {
			existing.Installed = true
			skills[dirKey] = existing
			continue
		}
		meta, err := readSkillMetadata(filepath.Join(ss.installDir, dir))
		name := strings.TrimSpace(meta.Name)
		desc := strings.TrimSpace(meta.Description)
		if err != nil || name == "" {
			name = dir
		}
		skills[dirKey] = Skill{
			Key:         buildSkillKey("", "", dir),
			Name:        name,
			Description: desc,
			Directory:   dir,
			ReadmeURL:   "",
			Installed:   true,
		}
	}
}

func (ss *SkillService) resolveReposForInstall(req installRequest, repos []skillRepoConfig) []skillRepoConfig {
	owner := strings.TrimSpace(req.RepoOwner)
	name := strings.TrimSpace(req.RepoName)
	var target []skillRepoConfig
	if owner != "" && name != "" {
		for _, repo := range repos {
			if !repo.Enabled {
				continue
			}
			if strings.EqualFold(repo.Owner, owner) && strings.EqualFold(repo.Name, name) {
				target = append(target, repo)
			}
		}
		return target
	}
	for _, repo := range repos {
		if repo.Enabled {
			target = append(target, repo)
		}
	}
	return target
}

func buildRepoURL(repo skillRepoConfig, branch, directory string) string {
	dir := strings.Trim(directory, "/")
	if dir == "" {
		return fmt.Sprintf("https://github.com/%s/%s", repo.Owner, repo.Name)
	}
	return fmt.Sprintf("https://github.com/%s/%s/tree/%s/%s", repo.Owner, repo.Name, branch, dir)
}

func buildSkillKey(owner, name, directory string) string {
	owner = strings.ToLower(strings.TrimSpace(owner))
	name = strings.ToLower(strings.TrimSpace(name))
	directory = strings.ToLower(directory)
	if owner == "" && name == "" {
		return fmt.Sprintf("local:%s", directory)
	}
	return fmt.Sprintf("%s/%s:%s", owner, name, directory)
}

func normalizeDirectoryKey(directory string) string {
	return strings.ToLower(strings.TrimSpace(directory))
}

func (ss *SkillService) isInstalled(directory string) bool {
	if ss.requireUserStorage() != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(ss.installDir, directory))
	return err == nil && info.IsDir()
}

func readSkillMetadata(dir string) (skillMetadata, error) {
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return skillMetadata{}, err
	}
	return parseSkillMetadata(string(data))
}

func parseSkillMetadata(content string) (skillMetadata, error) {
	var meta skillMetadata
	content = strings.TrimLeft(content, "\ufeff")

	// 使用 splitFrontMatter 替代 strings.SplitN，避免 YAML 值中的 --- 被误判
	_, fmLines, _, err := splitFrontMatter(content)
	if err != nil {
		return meta, errors.New("SKILL.md 缺少 front matter")
	}

	frontMatter := strings.Join(fmLines, "\n")
	if err := yaml.Unmarshal([]byte(frontMatter), &meta); err != nil {
		return meta, err
	}
	return meta, nil
}

func copyDirectory(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			if rel == "." {
				return os.MkdirAll(dst, 0o755)
			}
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}
