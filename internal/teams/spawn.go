
package teams

import (
	"context"
	"fmt"
	"os"
	"strings"

	"johnnycode/internal/agent"
	"johnnycode/internal/llm"
	"johnnycode/internal/tools"
)










type TeammateSpawnConfig struct {
	Team       *Team
	MemberName string
	Task       string
	Addendum   string

	Client   llm.Client
	Registry *tools.Registry
	Protocol string

	Workdir string
}




type SpawnResult struct {
	Mode    TeamMode
	EventCh <-chan agent.AgentEvent 
	PaneID  string                  
}







func SpawnTeammate(ctx context.Context, cfg TeammateSpawnConfig) (*SpawnResult, error) {
	if cfg.Team == nil {
		return nil, fmt.Errorf("SpawnTeammate: team is required")
	}
	if cfg.MemberName == "" {
		return nil, fmt.Errorf("SpawnTeammate: member name is required")
	}

	switch cfg.Team.Mode {
	case ModeInProcess:
		ch := StartInProcessMember(
			ctx,
			cfg.Team,
			cfg.MemberName,
			cfg.Client,
			cfg.Registry,
			cfg.Protocol,
			cfg.Task,
			cfg.Addendum,
		)
		
		
		if cfg.Workdir != "" {
			if m, ok := cfg.Team.Members[cfg.MemberName]; ok && m.AgentRef != nil {
				m.AgentRef.WorkDir = cfg.Workdir
			}
		}
		return &SpawnResult{Mode: ModeInProcess, EventCh: ch}, nil

	case ModeTmux:
		
		
		if cfg.Task != "" {
			_ = cfg.Team.MailBox.Send(cfg.MemberName, FileMailMessage{
				From: LeadName,
				Text: cfg.Task,
			})
		}
		cliCommand, err := BuildTeammateCLI(cfg.Team.Name, cfg.MemberName, cfg.Workdir)
		if err != nil {
			return nil, err
		}
		paneID, err := spawnTmuxTeammate(cfg.Team.Name, cfg.MemberName, cliCommand)
		if err != nil {
			return nil, err
		}
		cfg.Team.recordExternalMember(cfg.MemberName, paneID)
		return &SpawnResult{Mode: ModeTmux, PaneID: paneID}, nil

	case ModeITerm:
		if cfg.Task != "" {
			_ = cfg.Team.MailBox.Send(cfg.MemberName, FileMailMessage{
				From: LeadName,
				Text: cfg.Task,
			})
		}
		cliCommand, err := BuildTeammateCLI(cfg.Team.Name, cfg.MemberName, cfg.Workdir)
		if err != nil {
			return nil, err
		}
		tabID, err := spawnITermTeammate(cfg.Team.Name, cfg.MemberName, cliCommand)
		if err != nil {
			return nil, err
		}
		cfg.Team.recordExternalMember(cfg.MemberName, tabID)
		return &SpawnResult{Mode: ModeITerm, PaneID: tabID}, nil
	}

	return nil, fmt.Errorf("unknown team mode: %s", cfg.Team.Mode)
}





func (t *Team) recordExternalMember(name, paneID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Members[name] = &Member{
		Name:   name,
		Active: true,
		PaneID: paneID,
	}
}







func BuildTeammateCLI(teamName, memberName, workdir string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate johnnycode binary: %w", err)
	}
	if workdir == "" {
		workdir, _ = os.Getwd()
	}
	return fmt.Sprintf(
		"cd %s && %s --teammate --team-name %s --agent-name %s",
		shellQuote(workdir),
		shellQuote(exe),
		shellQuote(teamName),
		shellQuote(memberName),
	), nil
}



func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n'\"\\$`") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
