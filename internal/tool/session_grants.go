package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// SessionGrants 保存当前 johnny 进程内的审批授权。它只在内存中，不写项目
// 规则文件，也不进入信任文件；进程退出即失效。主 Agent、子任务和 workflow
// 任务通过同一个 ApprovalGate 共享同一份授权，授权只看命令、工作目录和
// 路径，与发起者无关。
type SessionGrants struct {
	mu       sync.RWMutex
	commands []CommandGrant
	paths    []PathRule
}

// CommandGrant 是一条会话命令授权的键。
//
//   - Prefix 非空表示沿用“批准并记录”的命令前缀语义；WorkDir 是该段执行时
//     的规范工作目录。
//   - Argv 非空表示不可记录命令的精确授权：程序、全部参数和重定向必须与
//     用户看到并批准的完全一致，参数不同就重新询问。
//
// Prefix 与 Argv 只能有一个非空。所有字段在保存和匹配时都会复制，调用方
// 可以安全复用传入切片。
type CommandGrant struct {
	WorkDir   string   `json:"cwd,omitempty"`
	Prefix    []string `json:"prefix,omitempty"`
	Argv      []string `json:"argv,omitempty"`
	Redirects []string `json:"redirects,omitempty"`
}

const (
	maxSessionSummaryEntries = 20
	maxSessionSummaryRunes   = 200
	maxSessionLabelRunes     = 240
)

func NewSessionGrants() *SessionGrants {
	return &SessionGrants{}
}

// labelArg 给含空白、引号、反斜杠或控制字符的参数加引号，避免
// ["touch","a b"] 与 ["touch","a","b"] 显示成同一条。
func labelArg(argument string) string {
	if argument == "" {
		return `""`
	}
	needsQuote := strings.ContainsFunc(argument, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == '"' || r == '\'' || r == '\\'
	})
	if !needsQuote {
		return argument
	}
	return strconv.Quote(argument)
}

func labelArgs(arguments []string) string {
	parts := make([]string, len(arguments))
	for i, argument := range arguments {
		parts[i] = labelArg(argument)
	}
	return strings.Join(parts, " ")
}

// Label 生成审批面板和 /permissions 使用的有界单行描述。argv 参数逐个
// 转义，工作目录带 cwd= 前缀，重定向带“重定向”标记，避免把参数、目录和
// 重定向混在一起。
func (g CommandGrant) Label() string {
	var body string
	switch {
	case len(g.Prefix) > 0:
		body = "前缀 " + labelArgs(g.Prefix)
	case len(g.Argv) > 0:
		body = labelArgs(g.Argv)
		if len(g.Redirects) > 0 {
			body += " 重定向 " + labelArgs(g.Redirects)
		}
	default:
		return ""
	}
	if g.WorkDir != "" {
		body = "cwd=" + labelArg(g.WorkDir) + " " + body
	}
	return truncateSessionText(body, maxSessionSummaryRunes)
}

// normalizeSessionWorkDir 尽量把请求里的工作目录规范化成与保存时一致的
// 形式；无法解析时保留原值，让比较自然失败而不是放宽匹配。这里不能
// TrimSpace：以空格结尾的目录是合法目录，与实际兄弟目录必须区分。
func normalizeSessionWorkDir(dir string) string {
	if !filepath.IsAbs(dir) {
		return dir
	}
	if resolved, err := canonicalDirectory(dir); err == nil {
		return resolved
	}
	return dir
}

// canonicalGrant 校验并规范化一条命令授权。WorkDir 必须能规范化为目录：
// 前缀授权按工作目录区分，精确 argv 授权也按工作目录区分。TrimSpace 只
// 用于判断是否为空，实际路径保持原样，避免 "dir " 被当成 "dir"。
func canonicalGrant(grant CommandGrant) (CommandGrant, error) {
	if strings.TrimSpace(grant.WorkDir) == "" || !filepath.IsAbs(grant.WorkDir) {
		return CommandGrant{}, errors.New("会话授权缺少绝对工作目录")
	}
	resolvedDir, err := canonicalDirectory(grant.WorkDir)
	if err != nil {
		return CommandGrant{}, fmt.Errorf("会话授权工作目录不可用：%w", err)
	}
	switch {
	case len(grant.Prefix) == 0 && len(grant.Argv) == 0:
		return CommandGrant{}, errors.New("会话授权缺少命令前缀或 argv")
	case len(grant.Prefix) > 0 && len(grant.Argv) > 0:
		return CommandGrant{}, errors.New("会话授权不能同时包含命令前缀和 argv")
	}
	if len(grant.Prefix) > 0 {
		prefix := append([]string(nil), grant.Prefix...)
		if !validRecordablePrefix(prefix) {
			return CommandGrant{}, fmt.Errorf("会话授权的命令前缀不可记录：%s", strings.Join(prefix, " "))
		}
		if len(grant.Redirects) > 0 {
			return CommandGrant{}, errors.New("命令前缀授权不接受重定向，请使用精确 argv")
		}
		return CommandGrant{WorkDir: resolvedDir, Prefix: prefix}, nil
	}
	argv := append([]string(nil), grant.Argv...)
	if strings.TrimSpace(argv[0]) == "" {
		return CommandGrant{}, errors.New("会话授权的 argv 缺少程序名")
	}
	for _, argument := range argv {
		if strings.IndexByte(argument, 0) >= 0 {
			return CommandGrant{}, errors.New("会话授权的 argv 包含无效字符")
		}
	}
	redirects := append([]string(nil), grant.Redirects...)
	for _, redirect := range redirects {
		if strings.TrimSpace(redirect) == "" || strings.IndexByte(redirect, 0) >= 0 {
			return CommandGrant{}, errors.New("会话授权的重定向无效")
		}
	}
	return CommandGrant{WorkDir: resolvedDir, Argv: argv, Redirects: redirects}, nil
}

// key 使用 JSON 编码而不是分隔符拼接：argv 参数里可以包含 \x1f 等控制
// 字符，拼接会把 {"prog","a\x1fb"} 和 {"prog","a","b"} 误判为同一条授权。
func (g CommandGrant) key() string {
	key := struct {
		Kind      string   `json:"kind"`
		WorkDir   string   `json:"cwd"`
		Prefix    []string `json:"prefix,omitempty"`
		Argv      []string `json:"argv,omitempty"`
		Redirects []string `json:"redirects,omitempty"`
	}{Kind: "argv", WorkDir: g.WorkDir, Prefix: g.Prefix, Argv: g.Argv, Redirects: g.Redirects}
	if len(g.Prefix) > 0 {
		key.Kind = "prefix"
	}
	data, _ := json.Marshal(key)
	return string(data)
}

func (g CommandGrant) matches(want CommandGrant) bool {
	if g.WorkDir != normalizeSessionWorkDir(want.WorkDir) {
		return false
	}
	if len(g.Prefix) > 0 {
		return len(want.Prefix) > 0 && len(want.Argv) == 0 &&
			len(want.Redirects) == 0 && stringSlicesEqual(g.Prefix, want.Prefix)
	}
	if len(g.Argv) > 0 {
		return len(want.Argv) > 0 && len(want.Prefix) == 0 &&
			stringSlicesEqual(g.Argv, want.Argv) && stringSlicesEqual(g.Redirects, want.Redirects)
	}
	return false
}

// Store 校验并保存命令和路径授权；任何一个无效都会整体失败，避免出现
// 用户以为已授权、实际只保存了一部分的情况。返回新增的命令和路径数量。
func (s *SessionGrants) Store(commands []CommandGrant, paths []PathRule) (int, int, error) {
	if s == nil {
		return 0, 0, errors.New("会话授权存储不可用")
	}
	validCommands := make([]CommandGrant, 0, len(commands))
	for _, grant := range commands {
		canonical, err := canonicalGrant(grant)
		if err != nil {
			return 0, 0, err
		}
		validCommands = append(validCommands, canonical)
	}
	validPaths := make([]PathRule, 0, len(paths))
	for _, rule := range paths {
		canonical, err := canonicalPathRule(rule.Path, rule.Kind)
		if err != nil {
			return 0, 0, err
		}
		validPaths = append(validPaths, canonical)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	addedCommands := 0
	seenCommands := make(map[string]bool, len(s.commands)+len(validCommands))
	for _, grant := range s.commands {
		seenCommands[grant.key()] = true
	}
	for _, grant := range validCommands {
		key := grant.key()
		if seenCommands[key] {
			continue
		}
		seenCommands[key] = true
		s.commands = append(s.commands, grant)
		addedCommands++
	}
	addedPaths := 0
	seenPaths := make(map[string]bool, len(s.paths)+len(validPaths))
	for _, rule := range s.paths {
		seenPaths[rule.Path+"\x00"+string(rule.Kind)] = true
	}
	for _, rule := range validPaths {
		key := rule.Path + "\x00" + string(rule.Kind)
		if seenPaths[key] {
			continue
		}
		seenPaths[key] = true
		s.paths = append(s.paths, rule)
		addedPaths++
	}
	return addedCommands, addedPaths, nil
}

// Clear 清除全部会话授权并返回清除的数量。
func (s *SessionGrants) Clear() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	count := len(s.commands) + len(s.paths)
	s.commands = nil
	s.paths = nil
	return count
}

func (s *SessionGrants) Len() (commands, paths int) {
	if s == nil {
		return 0, 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.commands), len(s.paths)
}

// Snapshot 返回全部授权副本，调用方可以安全地展示或比较。
func (s *SessionGrants) Snapshot() (commands []CommandGrant, paths []PathRule) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneCommandGrants(s.commands), append([]PathRule(nil), s.paths...)
}

// LimitedSnapshot 只复制最多 limit 条授权，并返回未复制的数量。用于
// /permissions 这类有输出上限的展示，避免授权很多时复制整份内存。
func (s *SessionGrants) LimitedSnapshot(limit int) (commands []CommandGrant, paths []PathRule, omitted int) {
	if s == nil {
		return nil, nil, 0
	}
	if limit < 0 {
		limit = 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := len(s.commands) + len(s.paths)
	if total <= limit {
		return cloneCommandGrants(s.commands), append([]PathRule(nil), s.paths...), 0
	}
	for _, grant := range s.commands {
		if len(commands) >= limit {
			break
		}
		commands = append(commands, cloneCommandGrant(grant))
	}
	remaining := limit - len(commands)
	for _, rule := range s.paths {
		if remaining <= 0 {
			break
		}
		paths = append(paths, rule)
		remaining--
	}
	return commands, paths, total - len(commands) - len(paths)
}

// Matches 判断请求中列出的全部缺失命令和路径是否都已被会话授权覆盖。
// 请求没有列出任何缺失项时返回 false：那就不是一次会话授权命中，不能
// 凭“没有要求”自动放行。
func (s *SessionGrants) Matches(commands []CommandGrant, paths []PathRule) bool {
	if s == nil || (len(commands) == 0 && len(paths) == 0) {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, want := range commands {
		found := false
		for _, grant := range s.commands {
			if grant.matches(want) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, want := range paths {
		if !s.pathMatchesLocked(want) {
			return false
		}
	}
	return true
}

// ContainsPath 判断规范绝对路径是否已被会话路径授权覆盖。目录授权覆盖
// 子项，文件授权只覆盖文件本身。只读模式使用独立的 workspaceScope，不会
// 查询这里。调用方传入的原始路径不能先 TrimSpace 或 filepath.Clean：
// 尾随空格属于文件名，Clean 还会在解析符号链接前错误折叠 `..`。
func (s *SessionGrants) ContainsPath(path string) bool {
	if s == nil || path == "" || !filepath.IsAbs(path) {
		return false
	}
	resolved, err := resolveExistingAncestors(path)
	if err != nil {
		resolved = path
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pathMatchesLocked(PathRule{Path: resolved})
}

func (s *SessionGrants) pathMatchesLocked(want PathRule) bool {
	path := want.Path
	if path == "" {
		return false
	}
	if filepath.IsAbs(path) {
		// 与保存路径规则时使用同一套真实路径解析，避免符号链接写法不同导致
		// 已经授权的路径反复询问。不要把原始路径预先 Clean。
		if resolved, err := resolveExistingAncestors(path); err == nil {
			path = resolved
		}
	}
	for _, rule := range s.paths {
		switch rule.Kind {
		case PathKindDir:
			if pathWithin(rule.Path, path) {
				return true
			}
		case PathKindFile:
			if rule.Path == path {
				return true
			}
		}
	}
	return false
}

func cloneCommandGrant(grant CommandGrant) CommandGrant {
	return CommandGrant{
		WorkDir:   grant.WorkDir,
		Prefix:    append([]string(nil), grant.Prefix...),
		Argv:      append([]string(nil), grant.Argv...),
		Redirects: append([]string(nil), grant.Redirects...),
	}
}

func cloneCommandGrants(grants []CommandGrant) []CommandGrant {
	if len(grants) == 0 {
		return nil
	}
	cloned := make([]CommandGrant, len(grants))
	for i, grant := range grants {
		cloned[i] = cloneCommandGrant(grant)
	}
	return cloned
}

// SessionGrantSummary 生成 /permissions 的“本会话授权”一节。输出有固定
// 上限，避免授权很多时把终端刷满；单个条目也会截断。
func SessionGrantSummary(grants *SessionGrants) string {
	var builder strings.Builder
	builder.WriteString("本会话授权（仅当前进程，退出即失效）：\n")
	commands, paths, remaining := grants.LimitedSnapshot(maxSessionSummaryEntries)
	if len(commands) == 0 && len(paths) == 0 {
		builder.WriteString("  （无）\n")
		builder.WriteString("  清除：/permissions clear-session")
		return builder.String()
	}
	shown := 0
	if len(commands) > 0 {
		builder.WriteString("  命令：\n")
		for _, grant := range commands {
			shown++
			builder.WriteString(fmt.Sprintf("    %d. %s\n", shown, grant.Label()))
		}
	}
	if len(paths) > 0 {
		builder.WriteString("  路径：\n")
		for _, rule := range paths {
			shown++
			kind := "文件"
			if rule.Kind == PathKindDir {
				kind = "目录"
			}
			line := fmt.Sprintf("    %d. [%s] %s", shown, kind, labelArg(rule.Path))
			builder.WriteString(truncateSessionText(line, maxSessionSummaryRunes) + "\n")
		}
	}
	if remaining > 0 {
		builder.WriteString(fmt.Sprintf("    … 另有 %d 条未显示\n", remaining))
	}
	builder.WriteString("  清除：/permissions clear-session")
	return builder.String()
}

func truncateSessionText(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}
