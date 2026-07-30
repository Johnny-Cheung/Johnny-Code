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

// Layer 1: Dangerous command detection

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

	// Dangerous git operations — irreversible or destructive.
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

// Layer 2: Path sandbox

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

// Layer 3: Rule engine

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
	// LocalPath is the path to the project-level hand-written YAML rules
	// file.  These rules persist across sessions.
	LocalPath string

	// sessionRules are in-memory rules that survive only for the
	// lifetime of this process.  AllowAlways decisions are stored here
	// so they are scoped to the current session — no wildcard, exact
	// match only — and evaporate on exit.
	sessionRules []Rule
}

func (e *RuleEngine) Evaluate(toolName, content string) *RuleEffect {
	// Check persisted rules first (single level: project-local file).
	rules := loadRulesFile(e.LocalPath)
	for i := len(rules) - 1; i >= 0; i-- {
		if rules[i].Matches(toolName, content) {
			eff := rules[i].Effect
			return &eff
		}
	}
	// Check session rules last so they override persisted ones.
	for i := len(e.sessionRules) - 1; i >= 0; i-- {
		if e.sessionRules[i].Matches(toolName, content) {
			eff := e.sessionRules[i].Effect
			return &eff
		}
	}
	return nil
}

// AddSessionRule appends a rule that lives only in memory for the current
// process. Used by AllowAlways so the user's "don't ask again" choice
// lasts for the session but does not leak across restarts.
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

// Safe read-only commands that don't need permission

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
				return true
			}
		}
	}
	return false
}

// commandHasPathOutsideSandbox scans a Bash command string for arguments
// that look like file paths (absolute, relative with dots, or containing a
// separator) and checks them against the sandbox.  Returns true if any
// path-like argument falls outside the allowed roots, signalling that the
// command should not be auto-allowed even though its name is safe.
func commandHasPathOutsideSandbox(command string, sandbox *PathSandbox) bool {
	fields := strings.Fields(command)
	for _, f := range fields {
		// Skip flags: -x, --foo, etc.
		if strings.HasPrefix(f, "-") {
			continue
		}
		if !looksLikePath(f) {
			continue
		}
		// Expand ~ to the home directory before sandbox check (Go's
		// filepath.Abs does not understand shell tilde expansion).
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

// looksLikePath reports whether s is a plausible file-path argument
// (not a flag or a bare non-path word).
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

// Layer 4+5: Permission Checker (orchestrates all layers)

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

	// Layer 0: Plan mode plan-file write exception
	if c.Mode == ModePlan && cat == tools.CategoryWrite && isPlanFile(content, c.PlanFilePath) {
		return Decision{Effect: Allow, Reason: "Plan mode: plan file write allowed"}
	}

	// Layer 1: safe read-only commands (auto-allow)
	if cat == tools.CategoryCommand && IsSafeCommand(content) {
		// Even when the command name is safe, check path-like arguments
		// against the sandbox.  "cat /etc/passwd" is safe by name but
		// its argument points outside the project — don't auto-allow it.
		if c.Sandbox != nil && commandHasPathOutsideSandbox(content, c.Sandbox) {
			// Fall through to rule engine / mode / Ask instead of
			// auto-allowing.
		} else {
			return Decision{Effect: Allow, Reason: "Safe read-only command"}
		}
	}

	// Layer 2: dangerous command (Bash only)
	if cat == tools.CategoryCommand {
		hit, reason := DetectDangerous(content)
		if hit {
			return Decision{Effect: Deny, Reason: fmt.Sprintf("Dangerous command blocked: %s", reason)}
		}
	}

	// Layer 3: path sandbox (file tools)
	if (cat == tools.CategoryRead || cat == tools.CategoryWrite) && content != "" {
		ok, reason := c.Sandbox.Check(content)
		if !ok {
			return Decision{Effect: Deny, Reason: fmt.Sprintf("Path sandbox: %s", reason)}
		}
	}

	// Layer 4: rule engine
	ruleResult := c.RuleEngine.Evaluate(tool.Name(), content)
	if ruleResult != nil {
		if *ruleResult == RuleAllow {
			return Decision{Effect: Allow, Reason: "Permission rule: allow"}
		}
		return Decision{Effect: Deny, Reason: "Permission rule: deny"}
	}

	// Layer 4: permission mode
	effect := ModeDecide(c.Mode, cat)
	if effect == Allow {
		return Decision{Effect: Allow, Reason: fmt.Sprintf("Permission mode %s: allow", c.Mode)}
	}
	if effect == Deny {
		return Decision{Effect: Deny, Reason: fmt.Sprintf("Permission mode %s: deny", c.Mode)}
	}

	// Layer 5: ASK → HITL
	return Decision{Effect: Ask, Reason: "User confirmation required"}
}

func isPlanFile(targetPath, planPath string) bool {
	if planPath == "" || targetPath == "" {
		return false
	}
	// Try absolute path comparison
	absTarget, err1 := filepath.Abs(targetPath)
	absPlan, err2 := filepath.Abs(planPath)
	if err1 == nil && err2 == nil && absTarget == absPlan {
		return true
	}
	// Check if target ends with the plan file's relative suffix
	cleanTarget := filepath.Clean(targetPath)
	cleanPlan := filepath.Clean(planPath)
	if cleanTarget == cleanPlan {
		return true
	}
	// Base name match: LLM occasionally shortens file_path to just the base name.
	// The plan slug is randomly generated (adjective+noun+timestamp), so collision
	// with an unrelated file under the same name is extremely unlikely.
	if filepath.Base(cleanTarget) == filepath.Base(cleanPlan) {
		return true
	}
	return false
}
