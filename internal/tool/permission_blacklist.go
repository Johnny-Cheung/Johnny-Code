package tool

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// 固定危险后缀只用于覆盖“项目前缀即将自动放行”的情况。危险程序本身
// 不进入可记录集合，因此不在黑名单里重复列举。
var dangerousSuffixes = map[string][]string{
	"git": {
		"--output", "--open-files-in-pager", "--paginate", "--ext-diff",
		"--textconv", "--filters", "--no-index", "--exec-path", "--upload-pack",
		"--receive-pack", "-c", "--config-env", "--git-dir", "--work-tree",
	},
	"go": {
		"-exec", "-toolexec", "-vettool",
	},
	"cargo": {
		"--config", "--manifest-path",
	},
	"npm":  {"--prefix", "--script-shell", "--shell", "--cwd", "--dir"},
	"yarn": {"--prefix", "--script-shell", "--shell", "--cwd", "--dir"},
	"pnpm": {"--prefix", "--script-shell", "--shell", "--cwd", "--dir"},
	"bun":  {"--prefix", "--script-shell", "--shell", "--cwd", "--dir"},
	"make": {"-f", "--file", "-C", "--directory", "-I", "--include-dir", "--eval"},
}

var safeGitSubcommands = map[string]bool{
	"status": true, "log": true, "show": true, "diff": true, "blame": true,
	"grep": true, "ls-files": true, "ls-tree": true, "rev-parse": true,
	"describe": true, "shortlog": true, "cat-file": true, "show-ref": true,
	"for-each-ref": true, "merge-base": true, "fsck": true, "help": true,
	"version": true,
}

var safeGoSubcommands = map[string]bool{
	"test": true, "build": true, "run": true, "vet": true,
	"doc": true, "list": true, "version": true,
}

var safeCargoSubcommands = map[string]bool{
	"test": true, "build": true, "run": true, "check": true, "clippy": true,
	"bench": true, "doc": true, "metadata": true, "tree": true, "version": true,
}

var safePackageSubcommands = map[string]bool{
	"run": true, "test": true, "start": true,
}

// dangerousSuffixHit 返回命中的危险后缀。匹配在 token 层进行，并尊重
// "--" 之后的操作数；它只决定是否取消自动放行，不是安全边界。
func dangerousSuffixHit(program string, tokens []string) (string, bool) {
	program = filepath.Base(program)
	patterns := dangerousSuffixes[program]
	if len(patterns) == 0 || len(tokens) == 0 {
		return "", false
	}
	for _, token := range tokens[1:] {
		if token == "--" {
			break
		}
		for _, pattern := range patterns {
			if token == pattern || strings.HasPrefix(token, pattern+"=") {
				return pattern, true
			}
		}
	}
	return "", false
}

// recordablePrefix 只对固定工具族的安全子命令生成前缀。其他命令只能
// 允许本次或拒绝，避免维护一份无限增长的程序级黑名单。
func recordablePrefix(tokens []string) ([]string, bool) {
	if len(tokens) < 2 || tokens[0] == "" || strings.ContainsRune(tokens[0], filepath.Separator) {
		return nil, false
	}
	program, subcommand := tokens[0], tokens[1]
	if strings.HasPrefix(subcommand, "-") {
		return nil, false
	}
	switch program {
	case "git":
		if safeGitSubcommands[subcommand] {
			return append([]string(nil), tokens[:2]...), true
		}
	case "go":
		if safeGoSubcommands[subcommand] {
			return append([]string(nil), tokens[:2]...), true
		}
	case "cargo":
		if safeCargoSubcommands[subcommand] {
			return append([]string(nil), tokens[:2]...), true
		}
	case "npm", "yarn", "pnpm", "bun":
		if !safePackageSubcommands[subcommand] && !(program != "npm" && subcommand == "build") {
			return nil, false
		}
		if subcommand == "run" {
			if len(tokens) < 3 || tokens[2] == "" || strings.HasPrefix(tokens[2], "-") {
				return nil, false
			}
			return append([]string(nil), tokens[:3]...), true
		}
		return append([]string(nil), tokens[:2]...), true
	case "make":
		return append([]string(nil), tokens[:2]...), true
	}
	return nil, false
}

func validRecordablePrefix(prefix []string) bool {
	got, ok := recordablePrefix(prefix)
	if !ok || len(got) != len(prefix) {
		return false
	}
	for i := range prefix {
		if got[i] != prefix[i] {
			return false
		}
	}
	return true
}

func prefixMatches(prefix, tokens []string) bool {
	if len(prefix) == 0 || len(tokens) < len(prefix) {
		return false
	}
	for i := range prefix {
		if prefix[i] != tokens[i] {
			return false
		}
	}
	return true
}

func dangerousSuffixLines() []string {
	programs := make([]string, 0, len(dangerousSuffixes))
	for program := range dangerousSuffixes {
		programs = append(programs, program)
	}
	sort.Strings(programs)
	lines := make([]string, 0, len(programs))
	for _, program := range programs {
		lines = append(lines, fmt.Sprintf("%s：%s", program, strings.Join(dangerousSuffixes[program], " ")))
	}
	return lines
}
