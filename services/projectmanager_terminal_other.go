//go:build !windows

package services

import "fmt"

func (s *ProjectManagerService) openProjectManagerSessionTerminal(session SessionSummary) error {
	return fmt.Errorf("当前平台暂未实现终端打开: %s", session.ID)
}
