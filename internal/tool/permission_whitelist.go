package tool

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// queryCommandSpec 是用户级只读白名单的固定表项。具体参数校验由 parse
// 完成；显示文本与匹配入口共用这张表，避免文档和代码各自维护一份名单。
type queryCommandSpec struct {
	name    string
	summary string
	parse   func([]string, queryPathResolver) (restrictedCommand, error)
}

var fixedQueryCommands = []queryCommandSpec{
	{name: "pwd", summary: "无参数", parse: parsePwd},
	{name: "ls", summary: "-a -l -h -1 -F -d，路径可省略", parse: parseLs},
	{name: "find", summary: "起始路径；-maxdepth/-type/-name/-print", parse: parseFind},
	{name: "grep", summary: "-n -i -r -E -F，pattern 和至少一个路径", parse: parseGrep},
	{name: "cat", summary: "-b -e -n -s -t -v，一个以上普通文件", parse: parseCat},
	{name: "head", summary: "-n N、-q -v，一个以上普通文件", parse: parseHead},
	{name: "tail", summary: "-n N 或 -n +N、-q -v，一个以上普通文件；拒绝 -f -F -r", parse: parseTail},
	{name: "wc", summary: "-l -w -c -m -L，一个以上普通文件", parse: parseWc},
	{name: "stat", summary: "一个以上路径，不接受选项", parse: parseStat},
	{name: "du", summary: "-a -c -h -k -m -s -x、-d 0..32，路径可省略", parse: parseDu},
	{name: "diff", summary: "-a -b -B -c -E -i -N -q -r -t -u -w、-U N，恰好两个路径", parse: parseDiff},
}

var fixedQueryCommandByName = func() map[string]queryCommandSpec {
	commands := make(map[string]queryCommandSpec, len(fixedQueryCommands))
	for _, command := range fixedQueryCommands {
		if _, exists := commands[command.name]; exists || command.parse == nil {
			panic("重复或无效的固定查询命令：" + command.name)
		}
		commands[command.name] = command
	}
	return commands
}()

func fixedQueryCommandLines() []string {
	lines := make([]string, 0, len(fixedQueryCommands))
	for _, command := range fixedQueryCommands {
		lines = append(lines, fmt.Sprintf("%s：%s", command.name, command.summary))
	}
	return lines
}

func fixedQueryCommandNames() []string {
	names := make([]string, 0, len(fixedQueryCommands))
	for _, command := range fixedQueryCommands {
		names = append(names, command.name)
	}
	sort.Strings(names)
	return names
}

func parseCat(tokens []string, scope queryPathResolver) (restrictedCommand, error) {
	flags, paths, err := splitShortOptions(tokens, "benstv")
	if err != nil {
		return restrictedCommand{}, err
	}
	if len(paths) == 0 {
		return restrictedCommand{}, invalidArguments("只读模式的 cat 至少需要一个路径", nil)
	}
	if err := rejectStdinPaths(paths); err != nil {
		return restrictedCommand{}, err
	}
	resolved, err := resolveRegularQueryPaths(scope, paths)
	if err != nil {
		return restrictedCommand{}, err
	}
	return restrictedCommand{program: "cat", args: appendFlagsAndPaths(flags, resolved), paths: resolved}, nil
}

func parseHead(tokens []string, scope queryPathResolver) (restrictedCommand, error) {
	flags, paths, err := parseNFlags(tokens, false)
	if err != nil {
		return restrictedCommand{}, err
	}
	if len(paths) == 0 {
		return restrictedCommand{}, invalidArguments("只读模式的 head 至少需要一个路径", nil)
	}
	if err := rejectStdinPaths(paths); err != nil {
		return restrictedCommand{}, err
	}
	resolved, err := resolveRegularQueryPaths(scope, paths)
	if err != nil {
		return restrictedCommand{}, err
	}
	return restrictedCommand{program: "head", args: appendFlagsAndPaths(flags, resolved), paths: resolved}, nil
}

func parseTail(tokens []string, scope queryPathResolver) (restrictedCommand, error) {
	flags, paths, err := parseNFlags(tokens, true)
	if err != nil {
		return restrictedCommand{}, err
	}
	if len(paths) == 0 {
		return restrictedCommand{}, invalidArguments("只读模式的 tail 至少需要一个路径", nil)
	}
	if err := rejectStdinPaths(paths); err != nil {
		return restrictedCommand{}, err
	}
	resolved, err := resolveRegularQueryPaths(scope, paths)
	if err != nil {
		return restrictedCommand{}, err
	}
	return restrictedCommand{program: "tail", args: appendFlagsAndPaths(flags, resolved), paths: resolved}, nil
}

func parseWc(tokens []string, scope queryPathResolver) (restrictedCommand, error) {
	flags, paths, err := splitShortOptions(tokens, "lwcmL")
	if err != nil {
		return restrictedCommand{}, err
	}
	if len(paths) == 0 {
		return restrictedCommand{}, invalidArguments("只读模式的 wc 至少需要一个路径", nil)
	}
	if err := rejectStdinPaths(paths); err != nil {
		return restrictedCommand{}, err
	}
	resolved, err := resolveRegularQueryPaths(scope, paths)
	if err != nil {
		return restrictedCommand{}, err
	}
	return restrictedCommand{program: "wc", args: appendFlagsAndPaths(flags, resolved), paths: resolved}, nil
}

func parseStat(tokens []string, scope queryPathResolver) (restrictedCommand, error) {
	flags, paths, err := splitShortOptions(tokens, "")
	if err != nil {
		return restrictedCommand{}, err
	}
	if len(flags) != 0 {
		return restrictedCommand{}, permissionDenied("只读模式的 stat 不接受选项", nil)
	}
	if len(paths) == 0 {
		return restrictedCommand{}, invalidArguments("只读模式的 stat 至少需要一个路径", nil)
	}
	resolved, err := resolveQueryPaths(scope, paths)
	if err != nil {
		return restrictedCommand{}, err
	}
	return restrictedCommand{program: "stat", args: appendFlagsAndPaths(nil, resolved), paths: resolved}, nil
}

func parseDu(tokens []string, scope queryPathResolver) (restrictedCommand, error) {
	flags := make([]string, 0, 4)
	paths := make([]string, 0, 2)
	optionsEnded := false
	for index := 1; index < len(tokens); index++ {
		token := tokens[index]
		if !optionsEnded {
			if token == "--" {
				optionsEnded = true
				continue
			}
			if token == "-d" {
				if index+1 >= len(tokens) {
					return restrictedCommand{}, invalidArguments("只读模式的 du 缺少 -d 值", nil)
				}
				value, err := strconv.Atoi(tokens[index+1])
				if err != nil || value < 0 || value > 32 {
					return restrictedCommand{}, invalidArguments("只读模式的 du -d 必须在 0 到 32 之间", err)
				}
				flags = append(flags, token, strconv.Itoa(value))
				index++
				continue
			}
			if strings.HasPrefix(token, "-") && token != "-" {
				if strings.HasPrefix(token, "--") {
					return restrictedCommand{}, permissionDenied(fmt.Sprintf("只读模式的 du 不支持选项 %q", token), nil)
				}
				for _, option := range token[1:] {
					if !strings.ContainsRune("achkmsx", option) {
						return restrictedCommand{}, permissionDenied(fmt.Sprintf("只读模式的 du 不支持选项 %q", token), nil)
					}
				}
				flags = append(flags, token)
				continue
			}
		}
		paths = append(paths, token)
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}
	if err := rejectStdinPaths(paths); err != nil {
		return restrictedCommand{}, err
	}
	resolved, err := resolveQueryPaths(scope, paths)
	if err != nil {
		return restrictedCommand{}, err
	}
	return restrictedCommand{program: "du", args: appendFlagsAndPaths(flags, resolved), paths: resolved}, nil
}

func parseDiff(tokens []string, scope queryPathResolver) (restrictedCommand, error) {
	flags := make([]string, 0, 4)
	paths := make([]string, 0, 2)
	recursive := false
	optionsEnded := false
	for index := 1; index < len(tokens); index++ {
		token := tokens[index]
		if !optionsEnded {
			if token == "--" {
				optionsEnded = true
				continue
			}
			if token == "-U" {
				if index+1 >= len(tokens) {
					return restrictedCommand{}, invalidArguments("只读模式的 diff 缺少 -U 值", nil)
				}
				value, err := strconv.Atoi(tokens[index+1])
				if err != nil || value < 0 || value > 32 {
					return restrictedCommand{}, invalidArguments("只读模式的 diff -U 必须在 0 到 32 之间", err)
				}
				flags = append(flags, token, strconv.Itoa(value))
				index++
				continue
			}
			if strings.HasPrefix(token, "-") && token != "-" {
				if strings.HasPrefix(token, "--") {
					return restrictedCommand{}, permissionDenied(fmt.Sprintf("只读模式的 diff 不支持选项 %q", token), nil)
				}
				for _, option := range token[1:] {
					if !strings.ContainsRune("abBcEiNqrtuw", option) {
						return restrictedCommand{}, permissionDenied(fmt.Sprintf("只读模式的 diff 不支持选项 %q", token), nil)
					}
					if option == 'r' {
						recursive = true
					}
				}
				flags = append(flags, token)
				continue
			}
		}
		paths = append(paths, token)
	}
	if len(paths) != 2 {
		return restrictedCommand{}, invalidArguments("只读模式的 diff 需要恰好两个路径", nil)
	}
	if err := rejectStdinPaths(paths); err != nil {
		return restrictedCommand{}, err
	}
	resolved, err := resolveDiffPaths(scope, paths, recursive)
	if err != nil {
		return restrictedCommand{}, err
	}
	return restrictedCommand{program: "diff", args: appendFlagsAndPaths(flags, resolved), paths: resolved}, nil
}

// splitShortOptions 处理只接受短布尔选项的命令。选项可出现在操作数之间；
// "--" 之后全部按操作数处理，长选项和未知短选项在启动进程前拒绝。
func splitShortOptions(tokens []string, allowed string) ([]string, []string, error) {
	flags := make([]string, 0, 2)
	paths := make([]string, 0, 2)
	optionsEnded := false
	for _, token := range tokens[1:] {
		if !optionsEnded {
			if token == "--" {
				optionsEnded = true
				continue
			}
			if strings.HasPrefix(token, "-") && token != "-" {
				if strings.HasPrefix(token, "--") {
					return nil, nil, permissionDenied(fmt.Sprintf("只读模式不支持选项 %q", token), nil)
				}
				for _, option := range token[1:] {
					if !strings.ContainsRune(allowed, option) {
						return nil, nil, permissionDenied(fmt.Sprintf("只读模式不支持选项 %q", token), nil)
					}
				}
				flags = append(flags, token)
				continue
			}
		}
		paths = append(paths, token)
	}
	return flags, paths, nil
}

// parseNFlags 处理 head/tail 的 -n、-q、-v、--。allowPlus 为 true 时
// tail 的 -n +N 也允许；拒绝 -n 的负数形式，避免跨平台语义差异。
func parseNFlags(tokens []string, allowPlus bool) ([]string, []string, error) {
	flags := make([]string, 0, 2)
	paths := make([]string, 0, 2)
	optionsEnded := false
	for index := 1; index < len(tokens); index++ {
		token := tokens[index]
		if !optionsEnded {
			if token == "--" {
				optionsEnded = true
				continue
			}
			if token == "-n" {
				if index+1 >= len(tokens) {
					return nil, nil, invalidArguments("只读模式的 -n 缺少行数", nil)
				}
				original := tokens[index+1]
				value := original
				if allowPlus {
					value = strings.TrimPrefix(value, "+")
				}
				number, err := strconv.Atoi(value)
				if err != nil || number < 0 {
					return nil, nil, invalidArguments("只读模式的 -n 必须是非负整数", err)
				}
				flags = append(flags, token, original)
				index++
				continue
			}
			if strings.HasPrefix(token, "-") && token != "-" {
				if strings.HasPrefix(token, "--") {
					return nil, nil, permissionDenied(fmt.Sprintf("只读模式不支持选项 %q", token), nil)
				}
				for _, option := range token[1:] {
					if option != 'q' && option != 'v' {
						return nil, nil, permissionDenied(fmt.Sprintf("只读模式不支持选项 %q", token), nil)
					}
				}
				flags = append(flags, token)
				continue
			}
		}
		paths = append(paths, token)
	}
	return flags, paths, nil
}

func resolveRegularQueryPaths(scope queryPathResolver, paths []string) ([]string, error) {
	resolved, err := resolveQueryPaths(scope, paths)
	if err != nil {
		return nil, err
	}
	for _, target := range resolved {
		info, err := os.Stat(target)
		if err != nil {
			return nil, queryPathError(target, err)
		}
		if !info.Mode().IsRegular() {
			return nil, executionError(fmt.Sprintf("只读查询目标不是普通文件：%s", target), errNotRegularFile)
		}
	}
	return resolved, nil
}

func resolveDiffPaths(scope queryPathResolver, paths []string, recursive bool) ([]string, error) {
	resolved, err := resolveQueryPaths(scope, paths)
	if err != nil {
		return nil, err
	}
	for _, target := range resolved {
		info, err := os.Stat(target)
		if err != nil {
			return nil, queryPathError(target, err)
		}
		if info.Mode().IsRegular() || (recursive && info.IsDir()) {
			continue
		}
		if info.IsDir() {
			return nil, executionError("只读模式的 diff 比较目录需要 -r", nil)
		}
		return nil, executionError(fmt.Sprintf("只读查询目标不是普通文件：%s", target), errNotRegularFile)
	}
	return resolved, nil
}

func rejectStdinPaths(paths []string) error {
	for _, path := range paths {
		if path == "-" {
			return invalidArguments("只读模式不接受标准输入", nil)
		}
	}
	return nil
}

func appendFlagsAndPaths(flags, paths []string) []string {
	args := append([]string{}, flags...)
	args = append(args, "--")
	args = append(args, paths...)
	return args
}
