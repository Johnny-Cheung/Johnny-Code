
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

