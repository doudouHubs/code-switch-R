//go:build !windows

package services

import "fmt"

func (s *ProjectManagerService) openProjectManagerSessionTerminal(session SessionSummary) error {
	return fmt.Errorf("当前平台暂未实现终端打开: %s", session.ID)
}

func (s *ProjectManagerService) openProjectManagerProjectTerminal(projectPath string) error {
	return fmt.Errorf("当前平台暂未实现项目终端打开: %s", projectPath)
}

func (s *ProjectManagerService) runProjectManagerProjectCommand(projectPath string, command string) error {
	return fmt.Errorf("当前平台暂未实现项目运行指令: %s", projectPath)
}

func (s *ProjectManagerService) runProjectManagerAICommit(projectPath string) error {
	return fmt.Errorf("当前平台暂未实现 AI-Commit: %s", projectPath)
}
