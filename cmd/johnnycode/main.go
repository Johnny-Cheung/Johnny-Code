// 来源：公众号@小林coding
// 后端八股网站：xiaolincoding.com
// Agent网站：xiaolinnote.com
// 简历模版：jianli.xiaolinnote.com


package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"johnnycode/internal/config"
	"johnnycode/internal/hooks"
	"johnnycode/internal/tui"
)

func main() {
	// Teammate-worker mode: when this binary is launched by teams.BuildTeammateCLI from a tmux/iTerm
	// spawn, the first arg is --teammate. Worker mode has no TUI; it runs the in-process teammate loop
	// against the shared mailbox.
	if args, ok := parseTeammateFlags(os.Args[1:]); ok {
		if err := runTeammate(args); err != nil {
			fmt.Fprintf(os.Stderr, "teammate: %s\n", err)
			os.Exit(1)
		}
		return
	}

	cfg, err := config.LoadConfig("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}

	// Validate hook config before handing it to the TUI. Bad hooks would otherwise misbehave at
	// runtime (wrong event name silently never fires; missing required fields blow up mid-tool-call).
	// We drop the offending list and start with no hooks so a typo doesn't brick the session — match.
	validHooks := cfg.Hooks
	if err := hooks.Validate(validHooks); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: hook configuration is invalid, starting with no hooks:\n%s\n", err)
		validHooks = nil
	}

	m := tui.New(cfg.Providers, cfg.MCPServers, validHooks)
	p := tea.NewProgram(m)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
}

