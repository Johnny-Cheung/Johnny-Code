package tool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

var ErrApprovalUnavailable = errors.New("当前入口不支持交互审批")

// ApprovalRequest 是工具层交给审批界面的有界数据。Target/Command/Detail
// 只用于展示，授权判断已经由调用方完成。
type ApprovalRequest struct {
	ToolCallID     string
	Tool           string
	WorkDir        string
	Target         string
	Command        string
	Reason         string
	Detail         string
	RecordPrefixes [][]string
	RecordPaths    []PathRule
	// SessionCommands/SessionPaths 只列出尚未被项目规则或已有会话授权覆盖的
	// 缺失项。全部命中后可以“本会话内允许”；命中危险后缀或 remember 时
	// 调用方不填这两个字段，面板就不会提供会话选项。
	SessionCommands []CommandGrant
	SessionPaths    []PathRule
	// Origin 非零表示请求来自子 Agent，由 Default.approve 根据 context 填写。
	Origin Origin
}

func (r ApprovalRequest) CanRecord() bool {
	return len(r.RecordPrefixes) > 0 || len(r.RecordPaths) > 0
}

// CanSession 表示这次请求存在可以记住的会话授权目标。没有缺失项时不提供
// 会话选项，避免把“没有要求”当成已授权。
func (r ApprovalRequest) CanSession() bool {
	return len(r.SessionCommands) > 0 || len(r.SessionPaths) > 0
}

// SessionLabel 生成面板上的会话授权键摘要，长度有界。截断时明确写出
// “另有 N 个键”，避免用户以为面板只会有这些授权。
func (r ApprovalRequest) SessionLabel() string {
	var parts []string
	for _, grant := range r.SessionCommands {
		if label := grant.Label(); label != "" {
			parts = append(parts, label)
		}
	}
	for _, rule := range r.SessionPaths {
		kind := "文件"
		if rule.Kind == PathKindDir {
			kind = "目录"
		}
		parts = append(parts, kind+" "+labelArg(rule.Path))
	}
	if len(parts) == 0 {
		return ""
	}
	joined := strings.Join(parts, "；")
	if utf8.RuneCountInString(joined) <= maxSessionLabelRunes {
		return joined
	}
	var builder strings.Builder
	shown := 0
	for index, part := range parts {
		suffix := fmt.Sprintf("；另有 %d 个键", len(parts)-index-1)
		candidate := part
		if shown > 0 {
			candidate = "；" + part
		}
		if utf8.RuneCountInString(builder.String()+candidate+suffix) > maxSessionLabelRunes {
			break
		}
		builder.WriteString(candidate)
		shown = index + 1
	}
	if shown == 0 {
		return truncateSessionText(joined, maxSessionLabelRunes)
	}
	// 省略数量放在最前面：面板随后还会按终端宽度截断，放在末尾可能先被
	// 裁掉，让用户误以为授权键已经完整显示。
	return fmt.Sprintf("另有 %d 个键；%s", len(parts)-shown, builder.String())
}

func (r ApprovalRequest) RecordLabel() string {
	var parts []string
	for _, prefix := range r.RecordPrefixes {
		if len(prefix) > 0 {
			parts = append(parts, "命令前缀 "+strings.Join(prefix, " "))
		}
	}
	for _, rule := range r.RecordPaths {
		kind := "文件"
		if rule.Kind == PathKindDir {
			kind = "目录"
		}
		parts = append(parts, kind+" "+rule.Path)
	}
	return strings.Join(parts, "；")
}

type ApprovalDecision struct {
	Allow   bool
	Record  bool
	Session bool
	// SessionStored 表示审批界面已经在返回决策前把会话授权写入共享授权库。
	// Gate.Ask 收到 true 时不会再写一次，因此 /permissions clear-session 与
	// 决策返回之间的竞争不会把刚清除的授权复活。只有实际持有 Gate 的审批
	// 界面应设置它；普通 ApprovalFunc 仍由 Gate.Ask 负责保存。
	SessionStored bool
	Source        string
	Reason        string
}

// ApprovalSourceSession 标记审批界面用已有会话授权自动放行的决策。这类
// 自动命中不产生 permission 审计事件；授权创建时才记录 allow_session。
const ApprovalSourceSession = "session"

// ApprovalAudit 只记录审批决策，不包含工具完整输出。Prefix/Paths 是实际
// 记录成功的目标，用于排查“为什么后续自动放行”。SessionCommands/SessionPaths
// 只在创建会话授权（decision=allow_session）时填写，带工作目录和前 argv。
type ApprovalAudit struct {
	ToolCallID      string
	Tool            string
	Target          string
	Decision        string
	Source          string
	Reason          string
	RequestReason   string
	RequestDetail   string
	Prefixes        [][]string
	Paths           []PathRule
	SessionCommands []CommandGrant
	SessionPaths    []PathRule
	Origin          Origin
}

type ApprovalFunc func(context.Context, ApprovalRequest) (ApprovalDecision, error)
type AuditFunc func(ApprovalAudit)

// ApprovalGate 连接工具执行线程和 TUI。Set 在启动任务前调用；Ask 在工具
// 执行线程阻塞等待，ctx 取消时必须返回。Gate 同时持有进程内的会话授权，
// 主 Agent、子任务和 workflow 任务共享同一份授权。
type ApprovalGate struct {
	mu      sync.RWMutex
	ask     ApprovalFunc
	audit   AuditFunc
	wait    func(Origin, bool)
	session *SessionGrants
}

func NewApprovalGate() *ApprovalGate {
	return &ApprovalGate{session: NewSessionGrants()}
}

// Session 返回共享的会话授权库；零值 Gate 也会惰性初始化。
func (g *ApprovalGate) Session() *SessionGrants {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.session == nil {
		g.session = NewSessionGrants()
	}
	return g.session
}

// StoreSession 保存请求中列出的全部会话授权。只有显式选择“本会话内允许”
// 时才调用；保存失败不会影响本次允许。
func (g *ApprovalGate) StoreSession(request ApprovalRequest) error {
	if g == nil {
		return errors.New("审批入口不可用")
	}
	if !request.CanSession() {
		return errors.New("请求没有可保存的会话授权目标")
	}
	_, _, err := g.Session().Store(request.SessionCommands, request.SessionPaths)
	return err
}

// SessionMatches 判断请求列出的全部缺失命令和路径是否已被会话授权覆盖。
// 调用方应在进入审批前用它自动放行；命中不写审计事件。
func (g *ApprovalGate) SessionMatches(request ApprovalRequest) bool {
	if g == nil || !request.CanSession() {
		return false
	}
	return g.Session().Matches(request.SessionCommands, request.SessionPaths)
}

// TrySession 是 SessionMatches 的自动放行入口别名，便于调用方表达意图。
func (g *ApprovalGate) TrySession(request ApprovalRequest) bool {
	return g.SessionMatches(request)
}

// ClearSession 清除全部会话授权，返回清除的数量。
func (g *ApprovalGate) ClearSession() int {
	if g == nil {
		return 0
	}
	return g.Session().Clear()
}

func (g *ApprovalGate) Set(fn ApprovalFunc) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ask = fn
}

func (g *ApprovalGate) SetAudit(fn AuditFunc) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.audit = fn
}

// SetWaitObserver 注册子任务审批等待的观察者：来自子 Agent 的请求开始等待时
// 以 true 调用，等待结束（无论结果）以 false 调用。
func (g *ApprovalGate) SetWaitObserver(fn func(Origin, bool)) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.wait = fn
}

func (g *ApprovalGate) Ask(ctx context.Context, request ApprovalRequest) (ApprovalDecision, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ApprovalDecision{}, err
	}
	if g == nil {
		return ApprovalDecision{Source: "unavailable", Reason: ErrApprovalUnavailable.Error()}, nil
	}
	g.mu.RLock()
	fn, wait := g.ask, g.wait
	g.mu.RUnlock()
	if fn == nil {
		return ApprovalDecision{Source: "unavailable", Reason: ErrApprovalUnavailable.Error()}, nil
	}
	if wait != nil && !request.Origin.IsZero() {
		wait(request.Origin, true)
		defer wait(request.Origin, false)
	}
	decision, err := fn(ctx, request)
	if err != nil {
		return ApprovalDecision{}, err
	}
	if !decision.Allow {
		decision.Record = false
		decision.Session = false
		decision.SessionStored = false
	}
	if decision.Session {
		decision.Record = false
		switch {
		case !request.CanSession():
			decision.Session = false
			decision.SessionStored = false
		case decision.SessionStored:
			// 审批界面已经保存过；不能再写一次，否则 clear-session 与决策
			// 返回之间的竞争会复活授权。此时也接受已提交的用户决策，不再
			// 因 ctx 取消把它改成审计上的拒绝。
		default:
			if err := ctx.Err(); err != nil {
				// fn 返回后、写授权前请求已被取消：不保存会话授权。
				decision.Session = false
				return ApprovalDecision{}, err
			}
			if err := g.StoreSession(request); err != nil {
				// 授权保存失败时仍保留本次允许，但明确告知用户没有记住。
				decision.Session = false
				if decision.Reason == "" {
					decision.Reason = "会话授权失败：" + err.Error()
				} else {
					decision.Reason += "；会话授权失败：" + err.Error()
				}
			} else {
				decision.SessionStored = true
			}
		}
	} else {
		decision.SessionStored = false
	}
	if decision.Record && !request.CanRecord() {
		decision.Record = false
	}
	if decision.Source == "" {
		decision.Source = "approval"
	}
	if decision.Reason == "" {
		switch {
		case decision.Session:
			decision.Reason = "用户允许本会话"
		case decision.Allow:
			decision.Reason = "用户允许"
		default:
			decision.Reason = "用户拒绝"
		}
	}
	return decision, nil
}

func (g *ApprovalGate) Audit(request ApprovalRequest, decision ApprovalDecision) {
	if g == nil {
		return
	}
	// 使用已有会话授权自动放行与项目前缀自动放行一致，不写 permission 事件；
	// 工具调用本身仍会进入会话记录。创建新授权时即使来源标记为 session
	// 仍然要审计。
	if decision.Source == ApprovalSourceSession && !decision.Session {
		return
	}
	g.mu.RLock()
	audit := g.audit
	g.mu.RUnlock()
	auditDecision(audit, request, decision)
}

func auditDecision(audit AuditFunc, request ApprovalRequest, decision ApprovalDecision) {
	if audit == nil {
		return
	}
	result := "deny"
	switch {
	case !decision.Allow:
		result = "deny"
	case decision.Session:
		result = "allow_session"
	case decision.Record:
		result = "record"
	default:
		result = "allow"
	}
	var prefixes [][]string
	var paths []PathRule
	if decision.Record {
		prefixes = clonePrefixes(request.RecordPrefixes)
		paths = append([]PathRule(nil), request.RecordPaths...)
	}
	var sessionCommands []CommandGrant
	var sessionPaths []PathRule
	if decision.Session {
		sessionCommands = cloneCommandGrants(request.SessionCommands)
		sessionPaths = append([]PathRule(nil), request.SessionPaths...)
	}
	audit(ApprovalAudit{
		ToolCallID:      request.ToolCallID,
		Tool:            request.Tool,
		Target:          approvalTarget(request),
		Decision:        result,
		Source:          decision.Source,
		Reason:          decision.Reason,
		RequestReason:   request.Reason,
		RequestDetail:   request.Detail,
		Prefixes:        prefixes,
		Paths:           paths,
		SessionCommands: sessionCommands,
		SessionPaths:    sessionPaths,
		Origin:          request.Origin,
	})
}

func approvalTarget(request ApprovalRequest) string {
	if request.Target != "" {
		return request.Target
	}
	if request.Command != "" {
		return request.Command
	}
	return request.RecordLabel()
}

func approvalDeniedMessage(request ApprovalRequest, decision ApprovalDecision) string {
	reason := strings.TrimSpace(decision.Reason)
	if reason == "" {
		reason = "用户拒绝"
	}
	return fmt.Sprintf("权限审批未通过：%s（%s）", reason, approvalTarget(request))
}
