package tool

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// queryTier 说明固定查询程序属于哪一档：纯读档本身没有写文件或执行其他程序的
// 参数；逃逸口档存在这类参数，只开放封闭的参数白名单和语义解析。
type queryTier int

const (
	tierPureRead queryTier = iota + 1
	tierEscape
)

func (t queryTier) String() string {
	if t == tierEscape {
		return "逃逸口档"
	}
	return "纯读档"
}

// queryCommandSpec 是用户级只读白名单的固定表项。具体参数校验由 parse
// 完成；显示文本与匹配入口共用这张表，避免文档和代码各自维护一份名单。
// parse 的最后一个参数表示该命令段是否处于管道中（可以读取标准输入）。
type queryCommandSpec struct {
	name    string
	summary string
	tier    queryTier
	parse   func([]string, queryPathResolver, bool) (restrictedCommand, error)
}

var fixedQueryCommands = []queryCommandSpec{
	{name: "echo", tier: tierPureRead, summary: "字面量参数，不经过 shell 展开", parse: parseLiteral("echo")},
	{name: "basename", tier: tierPureRead, summary: "字面量参数", parse: parseLiteral("basename")},
	{name: "dirname", tier: tierPureRead, summary: "字面量参数", parse: parseLiteral("dirname")},
	{name: "pwd", tier: tierPureRead, summary: "无参数", parse: parsePwd},
	{name: "realpath", tier: tierPureRead, summary: "-e -m -s -L -P -q -z，一个以上已存在路径", parse: parseRealpath},
	{name: "ls", tier: tierPureRead, summary: "排序、递归、长格式等显示选项；拒绝 -L -H；路径可省略", parse: parseLs},
	{name: "grep", tier: tierPureRead, summary: "常用匹配与输出选项、-e、-A/-B/-C、--include/--exclude；拒绝 -R -f；管道中可读标准输入", parse: parseGrep},
	{name: "cat", tier: tierPureRead, summary: "显示选项；一个以上普通文件，管道中可读标准输入", parse: parseCat},
	{name: "head", tier: tierPureRead, summary: "-n N、-c N、-N、-q -v；一个以上普通文件，管道中可读标准输入", parse: parseHead},
	{name: "tail", tier: tierPureRead, summary: "-n N 或 -n +N、-c N、-N、-q -v；拒绝 -f -F -r；管道中可读标准输入", parse: parseTail},
	{name: "wc", tier: tierPureRead, summary: "-l -w -c -m -L；一个以上普通文件，管道中可读标准输入", parse: parseWc},
	{name: "stat", tier: tierPureRead, summary: "-c/-f 格式、-x -s -t；一个以上路径", parse: parseStat},
	{name: "du", tier: tierPureRead, summary: "-a -c -h -k -m -s -x -b、-d 0..32、--max-depth；路径可省略", parse: parseDu},
	{name: "diff", tier: tierPureRead, summary: "常用比较选项、-U N；恰好两个路径", parse: parseDiff},
	{name: "find", tier: tierEscape, summary: "多个起始路径；-name/-iname/-path/-type/-size/-mtime/-maxdepth/-mindepth/-print 和 ! -o ( ) 组合；拒绝 -delete -exec -fprint -L", parse: parseFind},
	{name: "rg", tier: tierEscape, summary: "常用搜索选项；执行时强制 --no-config；拒绝 --pre -z -L -f", parse: parseRg},
	{name: "sed", tier: tierEscape, summary: "-n 加行范围或正则范围的 p 打印脚本；拒绝 -i 和 w e r 命令", parse: parseSed},
	{name: "sort", tier: tierEscape, summary: "常用排序选项；拒绝 -o -T --compress-program --files0-from", parse: parseSort},
	{name: "uniq", tier: tierEscape, summary: "-c -d -u -i -f -s -w；至多一个输入文件，不接受输出文件", parse: parseUniq},
	{name: "tree", tier: tierEscape, summary: "常用显示选项、-L N、-I/-P；拒绝 -o -R -l --fromfile", parse: parseTree},
	{name: "file", tier: tierEscape, summary: "-b -i -k -h -n -N 和 --mime；拒绝 -C -m -f -L -p -s", parse: parseFile},
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
		lines = append(lines, fmt.Sprintf("%s（%s）：%s", command.name, command.tier, command.summary))
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

// splitShortOptions 与 parseNFlags 仍被只读管道消费者使用，统一分段器接入后删除。
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
