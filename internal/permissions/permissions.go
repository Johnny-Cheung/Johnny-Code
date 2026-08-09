package permissions

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"johnnycode/internal/tools"
)

type DecisionEffect string

const (
	Allow DecisionEffect = "allow"
	Deny  DecisionEffect = "deny"
	Ask   DecisionEffect = "ask"
)

type Decision struct {
	Effect DecisionEffect
	Reason string
}

type PermissionMode string

const (
	ModeDefault      PermissionMode = "default"
	ModeAcceptEdits  PermissionMode = "acceptEdits"
	ModePlan         PermissionMode = "plan"
	ModeBypass       PermissionMode = "bypassPermissions"
)

var modeMatrix = map[PermissionMode]map[tools.ToolCategory]DecisionEffect{
	ModeDefault:     {tools.CategoryRead: Allow, tools.CategoryWrite: Ask, tools.CategoryCommand: Ask},
	ModeAcceptEdits: {tools.CategoryRead: Allow, tools.CategoryWrite: Allow, tools.CategoryCommand: Ask},
	ModeBypass:      {tools.CategoryRead: Allow, tools.CategoryWrite: Allow, tools.CategoryCommand: Allow},
}

func ModeDecide(mode PermissionMode, category tools.ToolCategory) DecisionEffect {
	m, ok := modeMatrix[mode]
	if !ok {
		return Ask
	}
	return m[category]
}



type dangerousPattern struct {
	re     *regexp.Regexp
	reason string
}

var defaultDangerousPatterns = []dangerousPattern{
	{regexp.MustCompile(`rm\s+-[a-z]*r[a-z]*f[a-z]*\s+/\s*$`), "recursive force delete root"},
	{regexp.MustCompile(`mkfs\.`), "format disk"},
	{regexp.MustCompile(`dd\s+if=.*of=/dev/`), "direct write to disk device"},
	{regexp.MustCompile(`chmod\s+-R\s+777\s+/`), "recursive chmod root"},
	{regexp.MustCompile(`:\(\)\{\s*:\|:&\s*\};:`), "fork bomb"},
	{regexp.MustCompile(`curl\s+.*\|\s*(ba)?sh`), "pipe remote script"},
	{regexp.MustCompile(`wget\s+.*\|\s*(ba)?sh`), "pipe remote script"},
	{regexp.MustCompile(`>\s*/dev/sd`), "overwrite disk device"},

	
	{regexp.MustCompile(`git\s+push\s+.*--force`), "git force push (overwrites remote history)"},
	{regexp.MustCompile(`git\s+push\s+.*--delete`), "git delete remote branch"},
	{regexp.MustCompile(`git\s+reset\s+--hard`), "git hard reset (discards all local changes)"},
	{regexp.MustCompile(`git\s+clean\s+-[a-z]*f[a-z]*`), "git clean (deletes untracked files)"},
}

func DetectDangerous(command string) (bool, string) {
	for _, p := range defaultDangerousPatterns {
		if p.re.MatchString(command) {
			return true, p.reason
		}
	}
	return false, ""
}



type PathSandbox struct {
	allowedRoots []string
}

func NewPathSandbox(projectRoot string, extraAllowed ...string) *PathSandbox {
	root, _ := filepath.Abs(projectRoot)
	allowed := []string{root, os.TempDir()}
	for _, p := range extraAllowed {
		abs, _ := filepath.Abs(p)
		allowed = append(allowed, abs)
	}
	return &PathSandbox{allowedRoots: allowed}
}

func (s *PathSandbox) Check(path string) (bool, string) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, fmt.Sprintf("cannot resolve path: %s", path)
	}

	for _, root := range s.allowedRoots {
		if strings.HasPrefix(abs, root) {
			return true, ""
		}
	}
	return false, fmt.Sprintf("path %s outside sandbox", path)
}



type RuleEffect string

const (
	RuleAllow RuleEffect = "allow"
	RuleDeny  RuleEffect = "deny"
)

type Rule struct {
	ToolName string
	Pattern  string
	Effect   RuleEffect
}

func (r Rule) Matches(toolName, content string) bool {
	if r.ToolName != toolName {
		return false
	}
	matched, _ := filepath.Match(r.Pattern, content)
	return matched
}

type RuleEngine struct {
	
	
	LocalPath string

	
	
	
	
	sessionRules []Rule
}

func (e *RuleEngine) Evaluate(toolName, content string) *RuleEffect {
	
	rules := loadRulesFile(e.LocalPath)
	for i := len(rules) - 1; i >= 0; i-- {
		if rules[i].Matches(toolName, content) {
			eff := rules[i].Effect
			return &eff
		}
	}
	
	for i := len(e.sessionRules) - 1; i >= 0; i-- {
		if e.sessionRules[i].Matches(toolName, content) {
			eff := e.sessionRules[i].Effect
			return &eff
		}
	}
	return nil
}




func (e *RuleEngine) AddSessionRule(r Rule) {
	e.sessionRules = append(e.sessionRules, r)
}

func loadRulesFile(path string) []Rule {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var entries []struct {
		RuleStr string `yaml:"rule"`
		Effect  string `yaml:"effect"`
	}
	if err := yaml.Unmarshal(data, &entries); err != nil {
		return nil
	}
	var rules []Rule
	for _, e := range entries {
		if e.Effect != "allow" && e.Effect != "deny" {
			continue
		}
		r, err := parseRule(e.RuleStr, RuleEffect(e.Effect))
		if err != nil {
			continue
		}
		rules = append(rules, r)
	}
	return rules
}

var ruleRE = regexp.MustCompile(`^(\w+)\((.+)\)$`)

func parseRule(raw string, effect RuleEffect) (Rule, error) {
	m := ruleRE.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return Rule{}, fmt.Errorf("invalid rule syntax: %s", raw)
	}
	return Rule{ToolName: m[1], Pattern: m[2], Effect: effect}, nil
}



var safeCommandPrefixes = []string{
	"ls", "dir", "pwd", "echo", "cat", "head", "tail", "wc",
	"find", "which", "whereis", "whoami", "hostname", "uname",
	"date", "cal", "uptime", "df", "du", "free", "env", "printenv",
	"file", "stat", "readlink", "realpath", "basename", "dirname",
	"sort", "uniq", "tr", "cut", "awk", "sed", "grep", "egrep", "fgrep",
	"diff", "comm", "tee", "xargs", "true", "false", "test",
	"git status", "git log", "git diff", "git show", "git branch",
	"git tag", "git remote", "git rev-parse", "git ls-files",
	"git blame", "git stash list", "go version", "go env",
	"node -v", "npm -v", "npx", "python --version", "pip list",
	"cargo --version", "rustc --version",
}

func IsSafeCommand(command string) bool {
	cmd := strings.TrimSpace(command)
	for _, prefix := range safeCommandPrefixes {
		if cmd == prefix || strings.HasPrefix(cmd, prefix+" ") || strings.HasPrefix(cmd, prefix+"\t") {
			if !strings.Contains(cmd, ">") && !strings.Contains(cmd, "|") &&
				!strings.Contains(cmd, ";") && !strings.Contains(cmd, "&&") &&
				!strings.Contains(cmd, "$(") && !strings.Contains(cmd, "`") {
				// 白名单命令若携带写语义参数（如 sed -i、find -delete），
				// 就不是纯只读，不得自动放行——写操作必须走后续检查与询问。
				return isEffectivelyReadOnly(cmd)
			}
		}
	}
	return false
}

// 白名单只是"减少打扰"的便利闸，绝不能放行任何写操作。
// isEffectivelyReadOnly 负责排除命中前缀但实际会修改文件系统的用法。

// whitelistWriteTools 的命令无论带什么参数都会写文件或依赖管道输入。
var whitelistWriteTools = map[string]bool{
	"tee":   true, // tee 的所有参数都是写入目标文件
	"xargs": true, // 依赖 stdin 管道输入，白名单不覆盖
}

// whitelistWriteFlags 是白名单命令中具有写语义的参数。
var whitelistWriteFlags = map[string][]string{
	"sed":  {"-i", "--in-place"},
	"find": {"-delete"},
}

func isEffectivelyReadOnly(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	name := fields[0]
	args := fields[1:]
	if whitelistWriteTools[name] {
		return false
	}
	for _, flag := range whitelistWriteFlags[name] {
		for _, a := range args {
			if a == flag || strings.HasPrefix(a, flag+".") || strings.HasPrefix(a, flag+"=") {
				return false
			}
		}
	}
	return true
}






func commandHasPathOutsideSandbox(command string, sandbox *PathSandbox) bool {
	fields := strings.Fields(command)
	for _, f := range fields {
		
		if strings.HasPrefix(f, "-") {
			continue
		}
		if !looksLikePath(f) {
			continue
		}
		
		
		path := f
		if strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, path[2:])
			}
		}
		if ok, _ := sandbox.Check(path); !ok {
			return true
		}
	}
	return false
}



func looksLikePath(s string) bool {
	return strings.HasPrefix(s, "/") ||
		strings.HasPrefix(s, "./") ||
		strings.HasPrefix(s, "../") ||
		strings.HasPrefix(s, "~/") ||
		strings.Contains(s, "/") ||
		strings.HasPrefix(s, `\`) ||
		strings.Contains(s, `\`)
}

var contentFields = map[string]string{
	"Bash": "command", "ReadFile": "file_path", "WriteFile": "file_path",
	"EditFile": "file_path", "Glob": "pattern", "Grep": "pattern",
}

func ExtractContent(toolName string, args map[string]any) string {
	field, ok := contentFields[toolName]
	if !ok {
		return ""
	}
	v, _ := args[field].(string)
	return v
}



type Checker struct {
	Sandbox      *PathSandbox
	RuleEngine   *RuleEngine
	Mode         PermissionMode
	PlanFilePath string
}

func NewChecker(sandbox *PathSandbox, ruleEngine *RuleEngine, mode PermissionMode) *Checker {
	return &Checker{Sandbox: sandbox, RuleEngine: ruleEngine, Mode: mode}
}

func (c *Checker) Check(tool tools.Tool, args map[string]any) Decision {
	content := ExtractContent(tool.Name(), args)
	cat := tool.Category()

	
	if c.Mode == ModePlan && cat == tools.CategoryWrite && isPlanFile(content, c.PlanFilePath) {
		return Decision{Effect: Allow, Reason: "Plan mode: plan file write allowed"}
	}


	// 危险命令黑名单必须先于安全命令白名单：黑名单是安全关键闸，
	// 白名单只是减少打扰的便利闸。便利逻辑永远不能 gate 安全逻辑——
	// 否则将来白名单扩展（如加入 git push）会意外放行黑名单要拦的命令。
	if cat == tools.CategoryCommand {
		hit, reason := DetectDangerous(content)
		if hit {
			return Decision{Effect: Deny, Reason: fmt.Sprintf("Dangerous command blocked: %s", reason)}
		}
	}

	// 安全命令白名单：纯只读命令自动放行，减少打扰。
	if cat == tools.CategoryCommand && IsSafeCommand(content) {
		// 命令包含沙箱外路径：不自动放行，降级走后续的规则引擎与模式矩阵。
		if c.Sandbox != nil && commandHasPathOutsideSandbox(content, c.Sandbox) {

		} else {
			return Decision{Effect: Allow, Reason: "Safe read-only command"}
		}
	}

	
	if (cat == tools.CategoryRead || cat == tools.CategoryWrite) && content != "" {
		ok, reason := c.Sandbox.Check(content)
		if !ok {
			return Decision{Effect: Deny, Reason: fmt.Sprintf("Path sandbox: %s", reason)}
		}
	}

	
	ruleResult := c.RuleEngine.Evaluate(tool.Name(), content)
	if ruleResult != nil {
		if *ruleResult == RuleAllow {
			return Decision{Effect: Allow, Reason: "Permission rule: allow"}
		}
		return Decision{Effect: Deny, Reason: "Permission rule: deny"}
	}

	
	effect := ModeDecide(c.Mode, cat)
	if effect == Allow {
		return Decision{Effect: Allow, Reason: fmt.Sprintf("Permission mode %s: allow", c.Mode)}
	}
	if effect == Deny {
		return Decision{Effect: Deny, Reason: fmt.Sprintf("Permission mode %s: deny", c.Mode)}
	}

	
	return Decision{Effect: Ask, Reason: "User confirmation required"}
}

func isPlanFile(targetPath, planPath string) bool {
	if planPath == "" || targetPath == "" {
		return false
	}
	
	absTarget, err1 := filepath.Abs(targetPath)
	absPlan, err2 := filepath.Abs(planPath)
	if err1 == nil && err2 == nil && absTarget == absPlan {
		return true
	}
	
	cleanTarget := filepath.Clean(targetPath)
	cleanPlan := filepath.Clean(planPath)
	if cleanTarget == cleanPlan {
		return true
	}
	
	
	
	if filepath.Base(cleanTarget) == filepath.Base(cleanPlan) {
		return true
	}
	return false
}
