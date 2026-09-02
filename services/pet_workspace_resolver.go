package services

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// PetWorkspaceAgentReader 只暴露 workspace 解析需要的宠物 agent 配置，
// 避免 resolver 直接依赖 PetDAO 的其他持久化职责。
type PetWorkspaceAgentReader interface {
	LoadAgent(context.Context, string) (*PetAgentConfig, error)
}

// PetWorkspaceProjectReader 是项目管理器的最小读取边界。项目 ID 的事实源
// 仍然由 ProjectManagerService 持有，resolver 只负责按宠物绑定查找路径。
type PetWorkspaceProjectReader interface {
	ListProjects() ([]ProjectSummary, error)
}

// PetProjectWorkspaceResolver 将宠物持久化的 projectId 解析为项目管理器提供的
// 真实路径。它是主聊天和旧 AI service 共用的唯一绑定解析 owner。
type PetProjectWorkspaceResolver struct {
	agents   PetWorkspaceAgentReader
	projects PetWorkspaceProjectReader
}

func NewPetProjectWorkspaceResolver(
	agents PetWorkspaceAgentReader,
	projects PetWorkspaceProjectReader,
) *PetProjectWorkspaceResolver {
	return &PetProjectWorkspaceResolver{agents: agents, projects: projects}
}

var _ PetWorkspaceResolver = (*PetProjectWorkspaceResolver)(nil)

// Resolve 按 projectId -> ProjectSummary.Path 解析 workspace。
// projectFolder 只为旧迁移数据保留；一旦存在 projectId，就算旧路径仍然存在，
// 也必须以项目管理器的当前路径为准，避免项目重命名或宠物串绑时继续使用旧目录。
func (r *PetProjectWorkspaceResolver) Resolve(ctx context.Context, petID string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if r == nil || r.agents == nil {
		return "", fmt.Errorf("pet workspace agent reader is unavailable")
	}

	petID = strings.TrimSpace(petID)
	if petID == "" {
		return "", fmt.Errorf("pet workspace requires a pet id")
	}
	agent, err := r.agents.LoadAgent(ctx, petID)
	if err != nil {
		return "", fmt.Errorf("load pet workspace binding: %w", err)
	}
	if agent == nil {
		return "", nil
	}

	projectID := petWorkspaceString(agent.ProjectID)
	if projectID != "" {
		if r.projects == nil {
			return "", fmt.Errorf("pet workspace project reader is unavailable")
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		projects, err := r.projects.ListProjects()
		if err != nil {
			return "", fmt.Errorf("list pet workspace projects: %w", err)
		}
		for _, project := range projects {
			path := strings.TrimSpace(project.Path)
			if path == "" || !petWorkspaceProjectMatches(projectID, project) {
				continue
			}
			return filepath.Clean(path), nil
		}
		// projectId 是当前配置的事实源；不能因为项目列表暂时没有该项，
		// 就回退到可能已经过期的 projectFolder。
		return "", fmt.Errorf("bound project %q was not found", projectID)
	}

	// 旧版本只保存绝对路径。该分支只读取数据库中的历史字段，不读取聊天
	// 请求里的兼容字段，因此不会重新引入前端伪造 workspace 的安全边界。
	return petWorkspaceString(agent.ProjectFolder), nil
}

func petWorkspaceString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func petWorkspaceProjectMatches(binding string, project ProjectSummary) bool {
	for _, candidate := range []string{project.ID, project.Path} {
		if petWorkspacePathEqual(binding, candidate) {
			return true
		}
	}
	return false
}

func petWorkspacePathEqual(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if left == right {
		return true
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return false
}
