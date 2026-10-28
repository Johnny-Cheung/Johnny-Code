package subagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

// Spawn 实现 tool.Spawner：校验、登记文件范围、创建子会话并在后台启动，立即返回子任务 ID。
// 它在主 Agent 的工具执行期间被调用，fork 因此可以安全读取主会话的请求视图。
func (m *Manager) Spawn(_ context.Context, request tool.SpawnRequest) (string, error) {
	if (request.Kind != tool.SpawnFresh && request.Kind != tool.SpawnFork) || (request.Access != tool.SpawnRead && request.Access != tool.SpawnWrite) {
		return "", errors.New("子任务的 kind 或 access 无效")
	}
	if request.Access == tool.SpawnWrite && len(request.Paths) == 0 {
		return "", errors.New("写入类子任务必须声明 paths")
	}
	if m.cfg.Toolbox.Mode == ModeReadOnly && request.Access == tool.SpawnWrite {
		return "", errors.New("只读模式不允许派出写入类子任务（access=write），请改用 access=read")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", errors.New("子任务管理器已关闭")
	}
	if m.parent == nil {
		m.mu.Unlock()
		return "", errors.New("子任务管理器尚未绑定主会话")
	}
	if len(m.tasks) >= MaxConcurrent {
		m.mu.Unlock()
		return "", fmt.Errorf("同时最多 %d 个子任务，当前已有 %d 个在运行；请等其中一个完成后再派出，或直接自己完成", MaxConcurrent, len(m.tasks))
	}
	id, err := m.newIDLocked()
	if err != nil {
		m.mu.Unlock()
		return "", err
	}
	info := Info{
		ID: id, SessionID: fmt.Sprintf("%s-sub-%s", m.cfg.SessionID, id),
		Description: strings.Join(strings.Fields(request.Description), " "),
		Kind:        request.Kind, Access: request.Access, Paths: append([]string(nil), request.Paths...),
		Started: time.Now(), State: StateRunning,
	}
	if request.Access == tool.SpawnWrite {
		if err := m.claims.Claim(id, info.Description, info.Paths); err != nil {
			m.mu.Unlock()
			return "", err
		}
	}
	conv, prompt, err := m.newConversation(info, request)
	if err != nil {
		m.claims.Release(id)
		m.mu.Unlock()
		return "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &task{info: info, conv: conv, prompt: prompt, ctx: ctx, cancel: cancel}
	m.tasks[id] = t
	m.wg.Add(1)
	m.mu.Unlock()
	m.emit(Event{Type: EventStarted, Info: info})
	go m.run(t)
	return id, nil
}

func (m *Manager) newConversation(info Info, request tool.SpawnRequest) (*agent.Conversation, string, error) {
	tools, err := m.cfg.Toolbox.build(info.Kind, info.Access, info.ID)
	if err != nil {
		return nil, "", err
	}
	session := info.SessionID
	if info.Kind == tool.SpawnFork {
		child, err := m.parent.Fork(agent.ForkOptions{
			Tools: tools, SessionID: session, MaxTurns: m.cfg.MaxRequests, RequestTimeout: m.cfg.RequestTimeout,
		})
		if err != nil {
			return nil, "", err
		}
		return child, forkDirective(info, request.Prompt), nil
	}
	system := ""
	if m.cfg.System != nil {
		system = m.cfg.System(info)
	}
	view := contextview.NewManager(contextview.Config{Budget: m.cfg.Budget, WorkDir: m.cfg.WorkDir, SessionID: session})
	child := agent.NewConversation(agent.Options{
		Client: m.cfg.Client, Tools: tools, Model: m.cfg.Model, System: system,
		InteractionMode: m.cfg.InteractionMode, MaxTurns: m.cfg.MaxRequests,
		RequestTimeout: m.cfg.RequestTimeout, MaxOutputTokens: m.cfg.MaxOutputTokens,
		ContextView: view, Pinned: m.cfg.Pinned,
	})
	return child, request.Prompt, nil
}

// forkDirective 是 fork 子会话收到的第一条指令：说明身份、范围和报告要求。
func forkDirective(info Info, prompt string) string {
	var scope string
	if info.Access == tool.SpawnWrite {
		scope = "你可以修改文件，但只限这些路径：" + strings.Join(info.Paths, "、") + "；范围外的 write/edit 会被拒绝。"
	} else {
		scope = "你只能读取文件和执行只读查询命令，不能修改文件。"
	}
	return fmt.Sprintf("[子任务指令]\n你现在是从以上对话派生出的后台子任务 %s，与主对话并行运行。不要继续或总结主对话，也不要向用户提问，只完成下面的任务。"+
		"你看到的是派出那一刻的对话快照，之后主对话的进展你看不到。%s你不能使用 task 和 remember。\n"+
		"你的最终回复就是交给主 Agent 的报告：写清结论、涉及的文件、执行过的命令和未解决项；不要建议用户重启或切换权限模式。\n\n任务：\n%s",
		info.ID, scope, prompt)
}
