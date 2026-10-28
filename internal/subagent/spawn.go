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
// 没有空闲名额时直接拒绝，不排队；排队只用于工作流的一整步任务。
func (m *Manager) Spawn(_ context.Context, request tool.SpawnRequest) (string, error) {
	if err := m.validateRequest(request); err != nil {
		return "", err
	}
	m.mu.Lock()
	if err := m.readyLocked(); err != nil {
		m.mu.Unlock()
		return "", err
	}
	if len(m.queue) > 0 || len(m.tasks) >= MaxConcurrent {
		running, queued := len(m.tasks), len(m.queue)
		m.mu.Unlock()
		return "", fmt.Errorf("同时最多 %d 个子任务，当前已有 %d 个在运行、%d 个在排队；请等其中一个完成后再派出，或直接自己完成", MaxConcurrent, running, queued)
	}
	t, err := m.prepareLocked(request)
	if err != nil {
		m.mu.Unlock()
		return "", err
	}
	m.queue = append(m.queue, t)
	started := m.admitLocked()
	m.mu.Unlock()
	m.launch(started)
	return t.info.ID, nil
}

// spawnAll 原子地登记一批子任务：任何一个校验、范围登记或会话创建失败，整批回滚，不启动任何任务。
// 通过后按先进先出放进队列，有空闲名额的先启动，其余排队等待。
func (m *Manager) spawnAll(requests []tool.SpawnRequest) ([]string, error) {
	for _, request := range requests {
		if err := m.validateRequest(request); err != nil {
			return nil, err
		}
	}
	m.mu.Lock()
	if err := m.readyLocked(); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	batch := make([]*task, 0, len(requests))
	for _, request := range requests {
		t, err := m.prepareLocked(request)
		if err != nil {
			for _, prepared := range batch {
				m.discardLocked(prepared)
			}
			m.mu.Unlock()
			return nil, err
		}
		batch = append(batch, t)
	}
	ids := make([]string, len(batch))
	for i, t := range batch {
		ids[i] = t.info.ID
	}
	m.queue = append(m.queue, batch...)
	started := m.admitLocked()
	m.mu.Unlock()
	m.launch(started)
	return ids, nil
}

func (m *Manager) validateRequest(request tool.SpawnRequest) error {
	if (request.Kind != tool.SpawnFresh && request.Kind != tool.SpawnFork) || (request.Access != tool.SpawnRead && request.Access != tool.SpawnWrite) {
		return errors.New("子任务的 kind 或 access 无效")
	}
	if request.Access == tool.SpawnWrite && len(request.Paths) == 0 {
		return errors.New("写入类子任务必须声明 paths")
	}
	if m.cfg.Toolbox.Mode == ModeReadOnly && request.Access == tool.SpawnWrite {
		return errors.New("只读模式不允许派出写入类子任务（access=write），请改用 access=read")
	}
	return nil
}

func (m *Manager) readyLocked() error {
	if m.closed {
		return errors.New("子任务管理器已关闭")
	}
	if m.parent == nil {
		return errors.New("子任务管理器尚未绑定主会话")
	}
	return nil
}

// prepareLocked 为一个子任务分配 ID、登记文件范围并创建子会话，但不启动。
// 失败时已登记的范围会被释放。fork 的快照在这里取，排队的任务也使用派出时的对话。
func (m *Manager) prepareLocked(request tool.SpawnRequest) (*task, error) {
	id, err := m.newIDLocked()
	if err != nil {
		return nil, err
	}
	info := Info{
		ID: id, SessionID: fmt.Sprintf("%s-sub-%s", m.cfg.SessionID, id),
		Description: strings.Join(strings.Fields(request.Description), " "),
		Kind:        request.Kind, Access: request.Access, Paths: append([]string(nil), request.Paths...),
		State: StateQueued,
	}
	if request.Access == tool.SpawnWrite {
		if err := m.claims.Claim(id, info.Description, info.Paths); err != nil {
			return nil, err
		}
	}
	conv, prompt, err := m.newConversation(info, request)
	if err != nil {
		m.claims.Release(id)
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &task{info: info, conv: conv, prompt: prompt, ctx: ctx, cancel: cancel}, nil
}

// admitLocked 按先进先出把排队的任务补进空出的名额，返回需要在释放锁之后启动的任务。
func (m *Manager) admitLocked() []*task {
	var started []*task
	for !m.closed && len(m.queue) > 0 && len(m.tasks) < MaxConcurrent {
		t := m.queue[0]
		m.queue = m.queue[1:]
		t.info.Started = time.Now()
		t.info.State = StateRunning
		m.tasks[t.info.ID] = t
		m.wg.Add(1)
		started = append(started, t)
	}
	return started
}

// launch 发出启动事件并在后台运行任务。任务在 admitLocked 里已计入 wg。
func (m *Manager) launch(started []*task) {
	for _, t := range started {
		m.emit(Event{Type: EventStarted, Info: t.info})
		go m.run(t)
	}
}

// dispatch 在名额空出后启动排队中的任务。
func (m *Manager) dispatch() {
	m.mu.Lock()
	started := m.admitLocked()
	m.mu.Unlock()
	m.launch(started)
}

// discardLocked 丢弃一个尚未启动的任务：释放文件范围并关闭子会话。
func (m *Manager) discardLocked(t *task) {
	t.cancel()
	m.claims.Release(t.info.ID)
	_ = t.conv.Close()
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
