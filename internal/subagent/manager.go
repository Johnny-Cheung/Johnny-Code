// Package subagent 管理后台运行的子 Agent：并发上限、文件范围、结果回送和统一取消。
package subagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

const (
	MaxConcurrent      = 3
	DefaultMaxRequests = 30
	DefaultTimeLimit   = 20 * time.Minute
)

// State 是运行中子任务的可见状态。
type State string

const (
	StateRunning         State = "running"
	StateWaitingApproval State = "waiting_approval"
)

// Info 描述一个子任务，用于界面状态区和日志。
type Info struct {
	ID string
	// SessionID 是子任务会话记录和工具输出存档共用的 ID。
	SessionID   string
	Description string
	Kind        tool.SpawnKind
	Access      tool.SpawnAccess
	Paths       []string
	Started     time.Time
	State       State
}

type EventType string

const (
	EventStarted EventType = "started"
	EventState   EventType = "state"
	EventEnded   EventType = "ended"
)

// Event 由 Manager 在子任务启动、状态变化和结束时同步交付。
// 回调可能来自任意 goroutine，实现方必须线程安全且不得阻塞。
type Event struct {
	Type EventType
	Info Info
	// 以下字段仅 EventEnded 有值。Injected 为 false 表示结果没有进入主对话（被取消）。
	Status    agent.Status
	Error     string
	Partial   bool
	Requests  int
	Duration  time.Duration
	Truncated bool
	Injected  bool
	Report    string
}

// EventSink 接收子 Agent 的诊断事件，通常是该子任务独立的 JSONL 记录器。
type EventSink interface {
	Record(agent.Event) error
	Close() error
}

type Config struct {
	WorkDir         string
	SessionID       string
	InteractionMode string
	Client          model.Client
	Model           string
	MaxOutputTokens int
	Budget          contextview.Budget
	Toolbox         Toolbox
	// Pinned 为 fresh 子 Agent 加载 AGENTS 和记忆索引。
	Pinned agent.PinnedContext
	// System 返回 fresh 子 Agent 的系统提示；fork 沿用父会话的系统提示。
	System         func(Info) string
	MaxRequests    int
	RequestTimeout time.Duration
	TimeLimit      time.Duration
	// OpenLog 为子任务创建独立的诊断记录器，可为 nil。
	OpenLog func(Info) EventSink
	OnEvent func(Event)
}

type task struct {
	info    Info
	conv    *agent.Conversation
	prompt  string
	ctx     context.Context
	cancel  context.CancelFunc
	waiting int
}

// Manager 登记、限额和取消子任务，并把结果放进父会话的收件箱。
type Manager struct {
	cfg    Config
	claims *tool.FileClaims

	mu     sync.Mutex
	parent *agent.Conversation
	tasks  map[string]*task
	used   map[string]bool
	closed bool
	wg     sync.WaitGroup
}

func NewManager(cfg Config) (*Manager, error) {
	if cfg.WorkDir == "" {
		return nil, errors.New("子任务管理器需要工作目录")
	}
	if cfg.Client == nil || cfg.Model == "" || cfg.Toolbox.Registry == nil {
		return nil, errors.New("子任务管理器需要模型客户端、模型名和工具注册表")
	}
	if cfg.MaxRequests <= 0 {
		cfg.MaxRequests = DefaultMaxRequests
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = agent.DefaultRequestTimeout
	}
	if cfg.TimeLimit <= 0 {
		cfg.TimeLimit = DefaultTimeLimit
	}
	if cfg.MaxOutputTokens <= 0 {
		cfg.MaxOutputTokens = contextview.DefaultOutputTokens
	}
	if !cfg.Budget.Valid() {
		cfg.Budget = contextview.NewBudget(contextview.DefaultWindow, cfg.MaxOutputTokens)
	}
	manager := &Manager{cfg: cfg, claims: tool.NewFileClaims(), tasks: make(map[string]*task), used: make(map[string]bool)}
	manager.cfg.Toolbox.Claims = manager.claims
	if cfg.Toolbox.Gate != nil {
		cfg.Toolbox.Gate.SetWaitObserver(manager.setWaiting)
	}
	return manager, nil
}

// SetParent 绑定主会话。tools 是主会话实际使用的工具集合，fork 沿用其定义。
func (m *Manager) SetParent(parent *agent.Conversation, tools interface{ Definitions() []model.ToolDefinition }) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.parent = parent
	if tools != nil {
		m.cfg.Toolbox.Definitions = tools.Definitions
	}
}

// GuardMain 包装主 Agent 的工具集合：不能写入运行中写入类子任务声明的路径。
func (m *Manager) GuardMain(inner agent.Tools) (agent.Tools, error) {
	return tool.NewScopeGuard(inner, m.claims, "", m.cfg.WorkDir)
}

func (m *Manager) newIDLocked() (string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		var raw [2]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", err
		}
		id := hex.EncodeToString(raw[:])
		if !m.used[id] {
			m.used[id] = true
			return id, nil
		}
	}
	return "", errors.New("无法生成子任务 ID")
}
