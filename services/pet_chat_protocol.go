package services

import (
	"fmt"
	"strings"
	"time"
)

// BuildPetAgentPersona 必须与 frontend/src/components/Pet/petChatProtocol.ts
// 保持字节级一致；同一 Codex thread 的 persona fingerprint 变化会触发新 thread，
// 因此心跳不能自行发明另一份 system prompt。
func BuildPetAgentPersona(systemPrompt, petName string) string {
	configured := strings.TrimSpace(systemPrompt)
	normalizedName := strings.TrimSpace(petName)
	if normalizedName == "" {
		normalizedName = "Kapi"
	}
	base := configured
	if base == "" {
		base = "你是" + normalizedName + "，一个简短、友善、会记得当前对话的桌面宠物。"
	}
	return base + "\n\n" + buildPetPlanInstructionsText()
}

func buildPetPlanInstructionsText() string {
	return `<pet-plan-rules>
计划能力只在主人明确要求现在做、稍后做、到点提醒、每天或每周重复时使用；普通聊天不要输出计划。
计划标签中的宠物动作只有：feed, bathe, soak, play, sleep, work, study；这条规则只约束 <pet-plan> 协议，不限制 Codex 默认工具、MCP 或浏览器能力。主人要求执行工具操作时，按当前 Codex 配置正常调用，不要把工具结果伪装成普通聊天。
需要安排时，在最终回复末尾追加且只追加一个隐藏标签：<pet-plan>{"version":1,"title":"可选计划名","steps":[{"kind":"action","action":"feed","schedule":{"kind":"now"}},{"kind":"reminder","text":"开会","schedule":{"kind":"at","at":"2026-01-01T09:00:00","tz":"Asia/Shanghai"}}]}</pet-plan>
step.kind=action 时必须使用允许的 action；step.kind=reminder 时必须提供简短 text。schedule.kind 支持 now、delay（delaySeconds）、at（ISO 时间或毫秒时间戳）、every（everyMs）和 cron（标准 5/6 段表达式 + tz）。相对时间优先使用 delay；绝对时间使用当前运行时上下文中的本地时区。
如果日期、时间或动作含糊，先用普通短句追问，不要输出计划标签。协议版本固定为 1；协议字段变化时必须同步提升版本。
</pet-plan-rules>`
}

func buildPetRuntimeContextAt(now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	return fmt.Sprintf(
		"当前本地时间：%s；当前时区：%s。处理绝对时间时，优先使用该时区。",
		now.UTC().Format(time.RFC3339Nano),
		time.Local.String(),
	)
}
