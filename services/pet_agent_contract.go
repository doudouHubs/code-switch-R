package services

import "encoding/json"

// AgentSkill 是 Codex app-server 返回的安全 Skill 摘要。路径是本机绝对路径，
// 只用于下一轮 input 的原生 skill 项；runtime 在真正发送前仍会重新校验它，
// 不能把前端回传的路径直接当成可信输入。
type AgentSkill struct {
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
	ShortDescription string `json:"shortDescription,omitempty"`
	Path             string `json:"path"`
	Scope            string `json:"scope,omitempty"`
	Enabled          bool   `json:"enabled"`
}

// AgentSkillReference 是用户本轮选择的 Skill 最小引用。路径必须来自后端
// skills/list 的当前结果，runtime 不信任前端单独提交的绝对路径。
type AgentSkillReference struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type AgentSkillError struct {
	Message string `json:"message"`
	Path    string `json:"path"`
}

type AgentSkillListResult struct {
	ProjectID string            `json:"projectId"`
	Workspace string            `json:"workspace"`
	Skills    []AgentSkill      `json:"skills"`
	Errors    []AgentSkillError `json:"errors,omitempty"`
}

// AgentModel 是 model/list 的稳定前端摘要。原始模型目录可能持续扩展，
// 这里只投影聊天 UI 需要的字段，避免把 app-server 内部配置和 provider 凭据
// 直接暴露到 Wails 或浏览器 bridge。
type AgentModel struct {
	ID                     string   `json:"id"`
	Model                  string   `json:"model,omitempty"`
	DisplayName            string   `json:"displayName"`
	Description            string   `json:"description,omitempty"`
	Hidden                 bool     `json:"hidden"`
	IsDefault              bool     `json:"isDefault"`
	InputModalities        []string `json:"inputModalities,omitempty"`
	DefaultReasoningEffort string   `json:"defaultReasoningEffort,omitempty"`
}

type AgentModelListResult struct {
	ProjectID  string       `json:"projectId"`
	Workspace  string       `json:"workspace"`
	Models     []AgentModel `json:"models"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

// AgentCommandRequest 是所有入口共用的 Codex 控制命令契约。Command 只允许
// runtime 已实现的 app-server 方法，未知 slash 命令仍按普通用户文本发送。
type AgentCommandRequest struct {
	ProjectID      string   `json:"projectId"`
	ProjectName    string   `json:"projectName,omitempty"`
	PetID          string   `json:"petId"`
	RequestID      string   `json:"requestId,omitempty"`
	Source         string   `json:"source,omitempty"`
	SessionName    string   `json:"sessionName,omitempty"`
	Persona        string   `json:"persona,omitempty"`
	Command        string   `json:"command"`
	Args           []string `json:"args,omitempty"`
	ForceReload    bool     `json:"forceReload,omitempty"`
	Cursor         string   `json:"cursor,omitempty"`
	IncludeHidden  bool     `json:"includeHidden,omitempty"`
	Limit          int      `json:"limit,omitempty"`
	Delivery       string   `json:"delivery,omitempty"`
	ExpectedTurnID string   `json:"expectedTurnId,omitempty"`
	Input          string   `json:"input,omitempty"`
}

type AgentCommandResult struct {
	Command   string          `json:"command"`
	Accepted  bool            `json:"accepted"`
	RequestID string          `json:"requestId,omitempty"`
	Text      string          `json:"text,omitempty"`
	ThreadID  string          `json:"threadId,omitempty"`
	TurnID    string          `json:"turnId,omitempty"`
	Skills    []AgentSkill    `json:"skills,omitempty"`
	Models    []AgentModel    `json:"models,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

// PetAIInteractionKind 是 app-server 暂停 turn 等待用户决定的请求类别。
type PetAIInteractionKind string

const (
	PetAIInteractionApproval   PetAIInteractionKind = "approval"
	PetAIInteractionPermission PetAIInteractionKind = "permission"
	PetAIInteractionUserInput  PetAIInteractionKind = "user_input"
	PetAIInteractionMCPForm    PetAIInteractionKind = "mcp_form"
)

type PetAIInteractionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type PetAIInteractionQuestion struct {
	ID       string                   `json:"id"`
	Header   string                   `json:"header,omitempty"`
	Question string                   `json:"question"`
	Secret   bool                     `json:"isSecret,omitempty"`
	Other    bool                     `json:"isOther,omitempty"`
	Options  []PetAIInteractionOption `json:"options,omitempty"`
}

// PetAIInteraction 只包含展示交互所需的安全字段，不携带原始 command 输出、
// provider 配置或任何凭据。RawSchema 只用于 MCP 表单字段定义，不是可执行代码。
type PetAIInteraction struct {
	ID                 string                     `json:"id"`
	Kind               PetAIInteractionKind       `json:"kind"`
	Method             string                     `json:"method"`
	ThreadID           string                     `json:"threadId,omitempty"`
	TurnID             string                     `json:"turnId,omitempty"`
	ItemID             string                     `json:"itemId,omitempty"`
	CallID             string                     `json:"callId,omitempty"`
	Title              string                     `json:"title,omitempty"`
	Reason             string                     `json:"reason,omitempty"`
	Command            string                     `json:"command,omitempty"`
	CWD                string                     `json:"cwd,omitempty"`
	ServerName         string                     `json:"serverName,omitempty"`
	Message            string                     `json:"message,omitempty"`
	AvailableDecisions []string                   `json:"availableDecisions,omitempty"`
	Questions          []PetAIInteractionQuestion `json:"questions,omitempty"`
	RawSchema          map[string]any             `json:"requestedSchema,omitempty"`
}

// ResolveInteractionRequest 是前端对 pending server request 的唯一回传入口。
// 不同 Kind 只读取对应字段，runtime 会再次校验决策值和问题 ID。
type ResolveInteractionRequest struct {
	InteractionID string              `json:"interactionId"`
	Decision      string              `json:"decision,omitempty"`
	Action        string              `json:"action,omitempty"`
	Scope         string              `json:"scope,omitempty"`
	Permissions   map[string]any      `json:"permissions,omitempty"`
	Answers       map[string][]string `json:"answers,omitempty"`
	Content       map[string]any      `json:"content,omitempty"`
}
