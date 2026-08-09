package teams

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"johnnycode/internal/agent"
	"johnnycode/internal/conversation"
	"johnnycode/internal/llm"
	"johnnycode/internal/tools"
)

type TeamMode string

const (
	ModeInProcess TeamMode = "in-process"
	ModeTmux      TeamMode = "tmux"
)

func teamsBaseDir() string {
	if dir := os.Getenv("JOHNNYCODE_TEAMS_DIR"); dir != "" {
		return dir
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, ".johnnycode", "teams")
}

type Member struct {
	Name     string
	AgentRef *agent.Agent
	Conv     *conversation.Manager
	Active   bool
	Cancel   context.CancelFunc
	
	
	PaneID   string
	Progress *TeammateProgress
}

type Team struct {
	Name    string
	Mode    TeamMode
	Members map[string]*Member
	MailBox *FileMailBox
	mu      sync.Mutex
}

func NewTeam(name string, mode TeamMode) *Team {
	inboxDir := filepath.Join(teamsBaseDir(), name, "inboxes")
	return &Team{
		Name:    name,
		Mode:    mode,
		Members: make(map[string]*Member),
		MailBox: NewFileMailBox(inboxDir),
	}
}

func (t *Team) AddMember(name string, client llm.Client, registry *tools.Registry, protocol string) *Member {
	t.mu.Lock()
	defer t.mu.Unlock()

	ag := agent.New(client, registry, protocol)
	member := &Member{
		Name:     name,
		AgentRef: ag,
		Conv:     conversation.NewManager(),
		Active:   false,
	}
	t.Members[name] = member
	return member
}

func (t *Team) StartMember(ctx context.Context, name string, task string) (<-chan agent.AgentEvent, error) {
	t.mu.Lock()
	member, ok := t.Members[name]
	t.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("member not found: %s", name)
	}

	memberCtx, cancel := context.WithCancel(ctx)
	member.Active = true
	member.Cancel = cancel

	member.Conv.AddUserMessage(task)
	ch := member.AgentRef.Run(memberCtx, member.Conv)
	return ch, nil
}

func (t *Team) StopMember(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	member, ok := t.Members[name]
	if !ok {
		return
	}
	
	
	
	if member.PaneID != "" {
		switch t.Mode {
		case ModeTmux:
			stopTmuxTeammate(member.PaneID)
		case ModeITerm:
			stopITermTeammate(member.PaneID)
		}
	}
	if member.Cancel != nil {
		member.Cancel()
	}
	member.Active = false
}

func (t *Team) GetTeammateProgress() []*TeammateProgress {
	t.mu.Lock()
	defer t.mu.Unlock()
	var result []*TeammateProgress
	for _, m := range t.Members {
		if m.Progress != nil {
			result = append(result, m.Progress)
		}
	}
	return result
}

func (t *Team) SendMessage(from, to, content string) {
	t.MailBox.Send(to, FileMailMessage{
		From:      from,
		Text:      content,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	})
}

type TeamManager struct {
	mu    sync.Mutex
	teams map[string]*Team
}

func NewTeamManager() *TeamManager {
	return &TeamManager{teams: make(map[string]*Team)}
}

func (tm *TeamManager) CreateTeam(name string, mode TeamMode) *Team {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	team := NewTeam(name, mode)
	tm.teams[name] = team
	return team
}





func (tm *TeamManager) CreateTeamWith(team *Team) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.teams[team.Name] = team
}

func (tm *TeamManager) GetTeam(name string) *Team {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.teams[name]
}

func (tm *TeamManager) DeleteTeam(name string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if team, ok := tm.teams[name]; ok {
		for memberName := range team.Members {
			team.StopMember(memberName)
		}
		delete(tm.teams, name)
	}
}

func (tm *TeamManager) ListTeams() []string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	var names []string
	for name := range tm.teams {
		names = append(names, name)
	}
	return names
}

func (tm *TeamManager) GetAllTeammateProgress() []*TeammateProgress {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	var result []*TeammateProgress
	for _, team := range tm.teams {
		result = append(result, team.GetTeammateProgress()...)
	}
	return result
}

func (tm *TeamManager) CloseAll() {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for name, team := range tm.teams {
		for memberName := range team.Members {
			team.StopMember(memberName)
		}
		delete(tm.teams, name)
	}
}
