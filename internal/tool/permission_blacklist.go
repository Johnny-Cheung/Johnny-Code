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

// shortOptionsWithValues 记录短选项簇中会吞掉剩余字符作为参数值的选项。
// 它只用于区分 "-fFile" 里的 f 是选项、"-Wf" 里的 f 是 -W 的参数；长选项
// 的取值与 "--" 分隔符关系由 dashDashMayBeValue 保守处理。
var shortOptionsWithValues = map[string]string{
	"git":  "Cc",
	"make": "fCIoWjlO",
}

// dashDashMayBeValue 标记那些在选项等待参数时会把 "--" 当成参数值而不是
// 操作数分隔符的解析器。make（GNU getopt_long）和 go（flag）都如此：
//
//	make all -o -- --file=evil.mk
//	go test -run -- --toolexec=/nonexistent
//
// 这两条命令里的危险选项仍然生效，因此遇到这种 "--" 不能停止扫描。git 的
// parse-options 即使选项缺值也会把 "--" 当分隔符或直接报错，保持精确停止；
// 未逐一确认的包管理器也保守继续扫描，代价只是多询问一次。
var dashDashMayBeValue = map[string]bool{
	"go": true, "make": true, "cargo": true,
	"npm": true, "yarn": true, "pnpm": true, "bun": true,
}

// dangerousSuffixHit 返回命中的危险后缀。匹配在 token 层进行，并尊重
// "--" 之后的操作数；调用方无论在做前缀记录还是精确 argv 会话授权，都
// 应以它为准取消自动放行。它不是 OS 安全边界。
func dangerousSuffixHit(program string, tokens []string) (string, bool) {
	program = filepath.Base(program)
	patterns := dangerousSuffixes[program]
	if len(patterns) == 0 || len(tokens) <= 1 {
		return "", false
	}
	valueShorts := shortOptionsWithValues[program]
	for index := 1; index < len(tokens); index++ {
		token := tokens[index]
		if token == "--" {
			if dashDashMayBeValue[program] && looksLikeOption(tokens[index-1]) {
				// 这个 "--" 可能是上一个选项的值，后面仍是选项位置。
				continue
			}
			break
		}
		if hit, ok := dangerousTokenHit(patterns, valueShorts, token); ok {
			return hit, true
		}
	}
	return "", false
}

func looksLikeOption(token string) bool {
	return len(token) > 1 && token[0] == '-' && token != "--"
}

func dangerousTokenHit(patterns []string, valueShorts, token string) (string, bool) {
	switch {
	case strings.HasPrefix(token, "--"):
		return dangerousLongTokenHit(patterns, token)
	case looksLikeOption(token):
		return dangerousShortTokenHit(patterns, valueShorts, token)
	default:
		return "", false
	}
}

// dangerousLongTokenHit 同时覆盖精确长选项、--opt=value、GNU 风格的长选项
// 唯一前缀缩写（make --fil=evil.mk、npm --prefi=/tmp）以及 go 的 --exec
// 写法。git 当前拒绝长选项缩写，这里仍按保守策略匹配。
func dangerousLongTokenHit(patterns []string, token string) (string, bool) {
	name := token
	if index := strings.IndexByte(token, '='); index >= 0 {
		name = token[:index]
	}
	for _, pattern := range patterns {
		if strings.HasPrefix(pattern, "--") {
			if name == pattern || (len(name) > 2 && strings.HasPrefix(pattern, name)) {
				return pattern, true
			}
			continue
		}
		if len(pattern) > 2 {
			// go test 的单横线多字符选项也接受 --exec / --toolexec。
			doubleDash := "-" + pattern
			if token == doubleDash || strings.HasPrefix(token, doubleDash+"=") {
				return pattern, true
			}
		}
	}
	return "", false
}

// dangerousShortTokenHit 覆盖 -f、-fFile、-f=File、-sfFile 等形式。遇到会
// 取值的短选项就停止，避免把 -Wf 里的 f 当成选项。
func dangerousShortTokenHit(patterns []string, valueShorts, token string) (string, bool) {
	for _, pattern := range patterns {
		if token == pattern {
			return pattern, true
		}
		if len(pattern) > 2 && strings.HasPrefix(token, pattern+"=") {
			return pattern, true
		}
	}
	for index := 1; index < len(token); index++ {
		option := token[index]
		for _, pattern := range patterns {
			if len(pattern) == 2 && pattern[1] == option {
				return pattern, true
			}
		}
		if strings.IndexByte(valueShorts, option) >= 0 {
			return "", false
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
