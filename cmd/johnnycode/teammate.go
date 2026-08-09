package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"johnnycode/internal/agent"
	"johnnycode/internal/config"
	"johnnycode/internal/llm"
	"johnnycode/internal/teams"
	"johnnycode/internal/tools"
)

type teammateArgs struct {
	teamName   string
	memberName string
}

func parseTeammateFlags(args []string) (teammateArgs, bool) {
	var out teammateArgs
	if len(args) == 0 || args[0] != "--teammate" {
		return out, false
	}
	i := 1
	for i < len(args) {
		switch args[i] {
		case "--team-name":
			if i+1 < len(args) {
				out.teamName = args[i+1]
				i += 2
				continue
			}
		case "--agent-name":
			if i+1 < len(args) {
				out.memberName = args[i+1]
				i += 2
				continue
			}
		}
		i++
	}
	return out, true
}

func runTeammate(args teammateArgs) error {
	if args.teamName == "" || args.memberName == "" {
		return fmt.Errorf("--teammate requires --team-name and --agent-name")
	}

	cfg, err := config.LoadConfig("")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if len(cfg.Providers) == 0 {
		return fmt.Errorf("no providers configured")
	}
	provider := cfg.Providers[0]

	client, err := llm.NewClient(&provider, "")
	if err != nil {
		return fmt.Errorf("create LLM client: %w", err)
	}

	registry := tools.NewRegistry()
	for _, t := range builtinTeammateTools() {
		registry.Register(t)
	}

	team := teams.NewTeam(args.teamName, teams.ModeInProcess)
	teamMgr := teams.NewTeamManager()

	teamMgr.CreateTeamWith(team)
	registry.Register(&teams.SendMessageTool{TeamMgr: teamMgr, SenderName: args.memberName})

	member := team.AddMember(args.memberName, client, registry, provider.Protocol)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	addendum := teams.BuildTeammateAddendum(args.teamName, args.memberName, nil)

	fmt.Fprintf(os.Stderr, "[teammate %s/%s] booted, awaiting tasks\n", args.teamName, args.memberName)
	return teams.RunInProcessTeammate(ctx, team, member, "", addendum, streamEventsToStderr())
}

func builtinTeammateTools() []tools.Tool {
	return []tools.Tool{
		&tools.ReadFileTool{},
		&tools.WriteFileTool{},
		&tools.EditFileTool{},
		&tools.BashTool{},
		&tools.GlobTool{},
		&tools.GrepTool{},
	}
}

func streamEventsToStderr() chan<- agent.AgentEvent {
	ch := make(chan agent.AgentEvent, 32)
	go func() {
		for ev := range ch {
			switch e := ev.(type) {
			case agent.StreamText:
				fmt.Fprint(os.Stderr, e.Text)
			case agent.ToolUseEvent:
				fmt.Fprintf(os.Stderr, "\n[tool %s]\n", e.ToolName)
			case agent.ToolResultEvent:
				summary := strings.TrimSpace(e.Output)
				if len(summary) > 200 {
					summary = summary[:200] + "..."
				}
				fmt.Fprintf(os.Stderr, "[result] %s\n", summary)
			case agent.ErrorEvent:
				fmt.Fprintf(os.Stderr, "[error] %s\n", e.Message)
			case agent.LoopComplete:
				fmt.Fprintf(os.Stderr, "[turn done after %d steps]\n", e.TotalTurns)
			}
		}
	}()
	return ch
}
