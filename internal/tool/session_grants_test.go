package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func sessionDir(t *testing.T) string {
	t.Helper()
	dir, err := canonicalDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSessionGrantsPrefixAndExactArgvKeys(t *testing.T) {
	dir := sessionDir(t)
	other := sessionDir(t)
	grants := NewSessionGrants()
	addedCommands, addedPaths, err := grants.Store([]CommandGrant{
		{WorkDir: dir, Prefix: []string{"go", "test"}},
		{WorkDir: dir, Argv: []string{"./build.sh", "--fast"}, Redirects: []string{"2>&1"}},
	}, []PathRule{{Path: filepath.Join(dir, "outside"), Kind: PathKindDir}})
	if err != nil {
		t.Fatal(err)
	}
	if addedCommands != 2 || addedPaths != 1 {
		t.Fatalf("保存数量错误：%d %d", addedCommands, addedPaths)
	}

	if !grants.Matches([]CommandGrant{{WorkDir: dir, Prefix: []string{"go", "test"}}}, nil) {
		t.Fatal("相同前缀和工作目录应命中")
	}
	// 前缀授权沿用项目前缀语义，可以覆盖更长参数；这正是“批准并记录”的键。
	if !grants.Matches([]CommandGrant{{WorkDir: dir, Prefix: []string{"go", "test"}}}, nil) {
		t.Fatal("前缀授权应可重检")
	}
	if grants.Matches([]CommandGrant{{WorkDir: dir, Prefix: []string{"go", "build"}}}, nil) {
		t.Fatal("不同前缀不应命中")
	}
	if grants.Matches([]CommandGrant{{WorkDir: other, Prefix: []string{"go", "test"}}}, nil) {
		t.Fatal("不同工作目录的前缀不应命中")
	}

	matchingArgv := CommandGrant{WorkDir: dir, Argv: []string{"./build.sh", "--fast"}, Redirects: []string{"2>&1"}}
	if !grants.Matches([]CommandGrant{matchingArgv}, nil) {
		t.Fatal("相同 argv 和重定向应命中")
	}
	cases := []struct {
		name  string
		grant CommandGrant
	}{
		{"额外参数", CommandGrant{WorkDir: dir, Argv: []string{"./build.sh", "--fast", "--deploy"}, Redirects: []string{"2>&1"}}},
		{"参数不同", CommandGrant{WorkDir: dir, Argv: []string{"./build.sh", "--slow"}, Redirects: []string{"2>&1"}}},
		{"缺少重定向", CommandGrant{WorkDir: dir, Argv: []string{"./build.sh", "--fast"}}},
		{"重定向不同", CommandGrant{WorkDir: dir, Argv: []string{"./build.sh", "--fast"}, Redirects: []string{">/dev/null"}}},
		{"不同工作目录", CommandGrant{WorkDir: other, Argv: []string{"./build.sh", "--fast"}, Redirects: []string{"2>&1"}}},
	}
	for _, item := range cases {
		if grants.Matches([]CommandGrant{item.grant}, nil) {
			t.Fatalf("%s不应命中：%+v", item.name, item.grant)
		}
	}
}

func TestSessionGrantsRequireEveryMissingRequirement(t *testing.T) {
	dir := sessionDir(t)
	outside := filepath.Join(dir, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	grants := NewSessionGrants()
	commands := []CommandGrant{
		{WorkDir: dir, Prefix: []string{"go", "test"}},
		{WorkDir: dir, Argv: []string{"./build.sh"}},
	}
	paths := []PathRule{{Path: outside, Kind: PathKindDir}}
	if _, _, err := grants.Store(commands, paths); err != nil {
		t.Fatal(err)
	}
	if !grants.Matches(commands, paths) {
		t.Fatal("全部缺失项都授权后应命中")
	}
	if grants.Matches([]CommandGrant{{WorkDir: dir, Argv: []string{"./other.sh"}}}, paths) {
		t.Fatal("未授权的命令不应命中")
	}
	if grants.Matches(commands, []PathRule{{Path: filepath.Join(dir, "other"), Kind: PathKindDir}}) {
		t.Fatal("缺少一条路径授权不应命中")
	}
}

func TestSessionGrantsDoNotAutoAllowEmptyRequests(t *testing.T) {
	grants := NewSessionGrants()
	if grants.Matches(nil, nil) {
		t.Fatal("没有缺失项的请求不能被会话授权自动放行")
	}
	gate := NewApprovalGate()
	if err := gate.StoreSession(ApprovalRequest{
		SessionCommands: []CommandGrant{{WorkDir: sessionDir(t), Argv: []string{"echo", "x"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if gate.TrySession(ApprovalRequest{}) {
		t.Fatal("空请求不应命中会话授权")
	}
}

func TestSessionGrantsPathsAndContainsPath(t *testing.T) {
	dir := sessionDir(t)
	child := filepath.Join(dir, "child")
	if err := os.MkdirAll(filepath.Join(child, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	grants := NewSessionGrants()
	if _, _, err := grants.Store(nil, []PathRule{
		{Path: child, Kind: PathKindDir},
		{Path: file, Kind: PathKindFile},
	}); err != nil {
		t.Fatal(err)
	}
	if !grants.ContainsPath(filepath.Join(child, "nested", "x.txt")) {
		t.Fatal("目录授权应覆盖子项")
	}
	if !grants.ContainsPath(file) {
		t.Fatal("文件授权应覆盖文件本身")
	}
	if grants.ContainsPath(filepath.Join(dir, "other.txt")) {
		t.Fatal("目录授权不应覆盖目录外路径")
	}
	if !grants.Matches(nil, []PathRule{{Path: filepath.Join(child, "nested", "y.txt")}}) {
		t.Fatal("路径缺失项应被目录授权覆盖")
	}
	if grants.ContainsPath("relative/path") {
		t.Fatal("相对路径不属于会话路径授权")
	}
}

func TestSessionGrantsRejectInvalidAndDuplicateGrants(t *testing.T) {
	dir := sessionDir(t)
	grants := NewSessionGrants()
	invalid := [][]CommandGrant{
		{{Prefix: []string{"go", "test"}}},
		{{WorkDir: "relative", Argv: []string{"echo"}}},
		{{WorkDir: dir, Prefix: []string{"go", "test"}, Argv: []string{"go", "test"}}},
		{{WorkDir: dir, Prefix: []string{"./build.sh"}}},
		{{WorkDir: dir, Argv: nil}},
		{{WorkDir: dir, Argv: []string{" "}}},
		{{WorkDir: dir, Argv: []string{"echo"}, Redirects: []string{" "}}},
	}
	for _, commands := range invalid {
		if _, _, err := grants.Store(commands, nil); err == nil {
			t.Fatalf("无效授权应被拒绝：%+v", commands)
		}
	}
	if commands, paths := grants.Len(); commands != 0 || paths != 0 {
		t.Fatalf("整体失败不应保存一部分：%d %d", commands, paths)
	}
	if _, _, err := grants.Store(nil, []PathRule{{Path: "relative", Kind: PathKindFile}}); err == nil {
		t.Fatal("相对路径授权应被拒绝")
	}

	grant := CommandGrant{WorkDir: dir, Prefix: []string{"go", "test"}}
	for i := 0; i < 3; i++ {
		if _, _, err := grants.Store([]CommandGrant{grant}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if commands, _ := grants.Len(); commands != 1 {
		t.Fatalf("重复授权应去重：%d", commands)
	}
	if got := grants.Clear(); got != 1 {
		t.Fatalf("清除数量错误：%d", got)
	}
	if commands, paths := grants.Len(); commands != 0 || paths != 0 {
		t.Fatal("Clear 后仍保留授权")
	}
}

func TestSessionGrantSummaryIsBounded(t *testing.T) {
	dir := sessionDir(t)
	grants := NewSessionGrants()
	commands := make([]CommandGrant, 0, 30)
	for i := 0; i < 30; i++ {
		commands = append(commands, CommandGrant{
			WorkDir: dir,
			Argv:    []string{"echo", strings.Repeat("x", 20) + string(rune('a'+i))},
		})
	}
	if _, _, err := grants.Store(commands, nil); err != nil {
		t.Fatal(err)
	}
	text := SessionGrantSummary(grants)
	if !strings.Contains(text, "本会话授权") || !strings.Contains(text, "clear-session") {
		t.Fatalf("摘要缺少标题或清除提示：%q", text)
	}
	if !strings.Contains(text, "另有") {
		t.Fatalf("超出显示上限时应说明剩余数量：%q", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if len([]rune(line)) > maxSessionSummaryRunes+20 {
			t.Fatalf("摘要行过长：%d %q", len([]rune(line)), line)
		}
	}
}

func TestApprovalGateSessionRoundTripAndAudit(t *testing.T) {
	dir := sessionDir(t)
	outside := filepath.Join(dir, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	pathRule := PathRule{Path: outside, Kind: PathKindDir}
	request := ApprovalRequest{
		ToolCallID:      "call-session",
		Tool:            "bash",
		WorkDir:         dir,
		Target:          "./build.sh --fast 2>&1",
		Reason:          "命令不在白名单",
		Detail:          "命令不在固定查询表",
		SessionCommands: []CommandGrant{{WorkDir: dir, Argv: []string{"./build.sh", "--fast"}, Redirects: []string{"2>&1"}}},
		SessionPaths:    []PathRule{pathRule},
	}
	gate := NewApprovalGate()
	gate.Set(func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
		return ApprovalDecision{Allow: true, Session: true, Source: "tui", Reason: "本会话内允许"}, nil
	})
	var audits []ApprovalAudit
	var mu sync.Mutex
	gate.SetAudit(func(audit ApprovalAudit) {
		mu.Lock()
		defer mu.Unlock()
		audits = append(audits, audit)
	})
	decision, err := gate.Ask(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allow || !decision.Session || decision.Record {
		t.Fatalf("会话决策异常：%+v", decision)
	}
	if !gate.TrySession(request) {
		t.Fatal("保存后应命中会话授权")
	}
	gate.Audit(request, decision)
	mu.Lock()
	if len(audits) != 1 {
		mu.Unlock()
		t.Fatalf("授权创建应写一条审计：%d", len(audits))
	}
	audit := audits[0]
	mu.Unlock()
	if audit.Decision != "allow_session" || len(audit.SessionCommands) != 1 || len(audit.SessionPaths) != 1 {
		t.Fatalf("会话审计字段异常：%+v", audit)
	}
	if audit.SessionCommands[0].WorkDir != dir || audit.SessionCommands[0].Argv[1] != "--fast" {
		t.Fatalf("审计未带 cwd/argv：%+v", audit.SessionCommands[0])
	}
	if audit.RequestReason != "命令不在白名单" || audit.RequestDetail != "命令不在固定查询表" {
		t.Fatalf("审计未记录请求原因：%+v", audit)
	}

	// 命中已有授权自动放行时不写 permission 事件。
	gate.Audit(request, ApprovalDecision{Allow: true, Source: ApprovalSourceSession, Reason: "本会话授权"})
	mu.Lock()
	defer mu.Unlock()
	if len(audits) != 1 {
		t.Fatalf("自动命中不应新增审计：%d", len(audits))
	}
}

func TestApprovalGateRejectsSessionWithoutKey(t *testing.T) {
	gate := NewApprovalGate()
	gate.Set(func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
		return ApprovalDecision{Allow: true, Session: true}, nil
	})
	request := ApprovalRequest{Tool: "remember", Target: "x", Reason: "每次询问"}
	decision, err := gate.Ask(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allow || decision.Session {
		t.Fatalf("没有授权键时不能保存会话授权：%+v", decision)
	}
	if commands, paths := gate.Session().Len(); commands != 0 || paths != 0 {
		t.Fatalf("不应保存任何授权：%d %d", commands, paths)
	}
}

func TestMultiPathScopeUsesSessionPaths(t *testing.T) {
	runtime := testRuntime(t)
	rules, err := NewProjectRules(runtime.WorkDir, runtime.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := newMultiPathScope(runtime.WorkDir, rules)
	if err != nil {
		t.Fatal(err)
	}
	outside := sessionDir(t)
	target := filepath.Join(outside, "file.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := scope.resolve(target)
	if err != nil {
		t.Fatal(err)
	}
	if scope.contains(resolved) {
		t.Fatal("没有会话授权时外部路径不应在范围内")
	}
	grants := NewSessionGrants()
	if _, _, err := grants.Store(nil, []PathRule{{Path: outside, Kind: PathKindDir}}); err != nil {
		t.Fatal(err)
	}
	scope.setSession(grants)
	if !scope.contains(resolved) {
		t.Fatal("会话目录授权应让外部路径进入范围")
	}
}

func TestSessionGrantKeyDoesNotCollideOnControlCharacters(t *testing.T) {
	dir := sessionDir(t)
	grants := NewSessionGrants()
	first := CommandGrant{WorkDir: dir, Argv: []string{"prog", "a\x1fb"}}
	second := CommandGrant{WorkDir: dir, Argv: []string{"prog", "a", "b"}}
	added, _, err := grants.Store([]CommandGrant{first, second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Fatalf("包含控制字符的不同 argv 不应被去重：%d", added)
	}
	if !grants.Matches([]CommandGrant{first}, nil) {
		t.Fatal("第一个 argv 授权应命中")
	}
	if !grants.Matches([]CommandGrant{second}, nil) {
		t.Fatal("第二个 argv 授权应命中")
	}
	if commands, _ := grants.Len(); commands != 2 {
		t.Fatalf("应保存两条授权：%d", commands)
	}
}

func TestSessionGrantTrailingSpaceCwdDoesNotAuthorizeSibling(t *testing.T) {
	root := sessionDir(t)
	plain := filepath.Join(root, "dir")
	withSpace := filepath.Join(root, "dir ")
	for _, path := range []string{plain, withSpace} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	grants := NewSessionGrants()
	if _, _, err := grants.Store([]CommandGrant{{
		WorkDir: withSpace,
		Argv:    []string{"echo", "x"},
	}}, nil); err != nil {
		t.Fatal(err)
	}
	if !grants.Matches([]CommandGrant{{WorkDir: withSpace, Argv: []string{"echo", "x"}}}, nil) {
		t.Fatal("尾随空格目录本身应命中")
	}
	if grants.Matches([]CommandGrant{{WorkDir: plain, Argv: []string{"echo", "x"}}}, nil) {
		t.Fatal("尾随空格目录的授权不能命中兄弟目录")
	}
}

func TestSessionContainsPathResolvesSymlinkBeforeParent(t *testing.T) {
	root := sessionDir(t)
	real := filepath.Join(root, "real")
	sub := filepath.Join(real, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(real, "outside")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(sub, link); err != nil {
		t.Fatal(err)
	}
	candidate := root + string(filepath.Separator) + "link" +
		string(filepath.Separator) + ".." + string(filepath.Separator) + "outside"

	grants := NewSessionGrants()
	if _, _, err := grants.Store(nil, []PathRule{{Path: real, Kind: PathKindDir}}); err != nil {
		t.Fatal(err)
	}
	// 物理语义：link -> real/sub，link/.. -> real，因此候选路径是 real/outside。
	// 词法 Clean 会错误得到 root/outside，从而漏掉已有授权。
	if !grants.ContainsPath(candidate) {
		t.Fatalf("符号链接后的 .. 应按物理解析：%s", candidate)
	}
	if !grants.Matches(nil, []PathRule{{Path: candidate}}) {
		t.Fatalf("路径匹配也应使用物理解析：%s", candidate)
	}
}

func TestApprovalGateCanceledLateDecisionDoesNotStore(t *testing.T) {
	dir := sessionDir(t)
	request := ApprovalRequest{
		ToolCallID:      "cancel-late",
		Tool:            "bash",
		WorkDir:         dir,
		SessionCommands: []CommandGrant{{WorkDir: dir, Argv: []string{"echo", "x"}}},
	}
	gate := NewApprovalGate()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gate.Set(func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
		cancel()
		return ApprovalDecision{Allow: true, Session: true}, nil
	})
	decision, err := gate.Ask(ctx, request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("fn 返回后已取消应返回 context.Canceled：%#v %v", decision, err)
	}
	if decision.Allow || decision.Session {
		t.Fatalf("取消后不应返回允许决策：%#v", decision)
	}
	if commands, paths := gate.Session().Len(); commands != 0 || paths != 0 {
		t.Fatalf("取消后不应保存授权：%d %d", commands, paths)
	}
}

func TestApprovalGateCommittedSessionBeatsCancellation(t *testing.T) {
	dir := sessionDir(t)
	request := ApprovalRequest{
		ToolCallID:      "committed-then-cancel",
		Tool:            "bash",
		WorkDir:         dir,
		SessionCommands: []CommandGrant{{WorkDir: dir, Argv: []string{"echo", "x"}}},
	}
	gate := NewApprovalGate()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var storedErr error
	gate.Set(func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
		storedErr = gate.StoreSession(request)
		return ApprovalDecision{Allow: true, Session: true, SessionStored: true, Source: "tui"}, nil
	})
	decision, err := gate.Ask(ctx, request)
	if storedErr != nil {
		t.Fatal(storedErr)
	}
	if err != nil || !decision.Allow || !decision.Session || !decision.SessionStored {
		t.Fatalf("审批界面已提交并保存的决策不应被取消覆盖：%#v %v", decision, err)
	}
	if !gate.TrySession(request) {
		t.Fatal("已提交的授权应仍在")
	}
}

func TestApprovalGateStoredReceiptDoesNotResurrectAfterClear(t *testing.T) {
	dir := sessionDir(t)
	request := ApprovalRequest{
		ToolCallID:      "clear-while-awaiting",
		Tool:            "bash",
		WorkDir:         dir,
		SessionCommands: []CommandGrant{{WorkDir: dir, Argv: []string{"echo", "x"}}},
	}
	gate := NewApprovalGate()
	gate.Set(func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
		if err := gate.StoreSession(request); err != nil {
			return ApprovalDecision{}, err
		}
		if cleared := gate.ClearSession(); cleared == 0 {
			return ApprovalDecision{}, errors.New("测试前置授权未保存")
		}
		return ApprovalDecision{Allow: true, Session: true, SessionStored: true, Source: "tui"}, nil
	})
	decision, err := gate.Ask(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allow || !decision.Session || !decision.SessionStored {
		t.Fatalf("返回决策应保留已保存回执：%#v", decision)
	}
	if commands, paths := gate.Session().Len(); commands != 0 || paths != 0 {
		t.Fatalf("clear-session 后不应被 Gate.Ask 重新写回：%d %d", commands, paths)
	}
}

func TestCommandGrantLabelDisambiguatesArgsCwdAndRedirects(t *testing.T) {
	dir := sessionDir(t)
	spaced := CommandGrant{WorkDir: dir, Argv: []string{"touch", "a b"}}
	split := CommandGrant{WorkDir: dir, Argv: []string{"touch", "a", "b"}}
	if spaced.Label() == split.Label() {
		t.Fatalf("含空格参数与拆分参数不应显示成同一条：%q", spaced.Label())
	}
	if !strings.Contains(spaced.Label(), `"a b"`) {
		t.Fatalf("含空格参数应加引号：%q", spaced.Label())
	}
	if !strings.Contains(spaced.Label(), "cwd=") {
		t.Fatalf("标签应标明工作目录：%q", spaced.Label())
	}
	redirected := CommandGrant{WorkDir: dir, Argv: []string{"touch", "a"}, Redirects: []string{"2>&1"}}
	if !strings.Contains(redirected.Label(), "重定向 2>&1") {
		t.Fatalf("标签应区分重定向：%q", redirected.Label())
	}
	if redirected.Label() == (CommandGrant{WorkDir: dir, Argv: []string{"touch", "a"}}).Label() {
		t.Fatal("有重定向和无重定向的标签不应相同")
	}
}

func TestApprovalRequestSessionLabelReportsOmittedKeys(t *testing.T) {
	dir := sessionDir(t)
	request := ApprovalRequest{}
	for i := 0; i < 30; i++ {
		request.SessionCommands = append(request.SessionCommands, CommandGrant{
			WorkDir: dir,
			Argv:    []string{"echo", strings.Repeat("x", 8) + strconv.Itoa(i)},
		})
	}
	label := request.SessionLabel()
	if !strings.Contains(label, "另有") {
		t.Fatalf("截断后应说明还有多少键：%q", label)
	}
	if got := utf8.RuneCountInString(label); got > maxSessionLabelRunes {
		t.Fatalf("会话标签超过上限：%d %q", got, label)
	}
	small := ApprovalRequest{SessionCommands: request.SessionCommands[:1]}
	if strings.Contains(small.SessionLabel(), "另有") {
		t.Fatalf("未截断时不应出现“另有”：%q", small.SessionLabel())
	}
}
