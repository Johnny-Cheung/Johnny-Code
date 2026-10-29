package tool

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// find 属于逃逸口档：-delete、-exec*、-fprint* 等会写文件或执行程序，因此只开放
// 下列谓词。取值总是被当作模式消费，即使以 - 开头也不会成为新的谓词。
var (
	findSizePattern = regexp.MustCompile(`^[+-]?[0-9]{1,9}[cwbkMG]?$`)
	findTimePattern = regexp.MustCompile(`^[+-]?[0-9]{1,9}$`)
)

var findFlagPredicates = map[string]bool{
	"-print": true, "-print0": true, "-empty": true, "-prune": true,
	"!": true, "-not": true, "-a": true, "-and": true, "-o": true, "-or": true, "(": true, ")": true,
}

var findPatternPredicates = map[string]bool{"-name": true, "-iname": true, "-path": true, "-ipath": true}

var findTimePredicates = map[string]bool{
	"-mtime": true, "-atime": true, "-ctime": true, "-mmin": true, "-amin": true, "-cmin": true,
}

func isFindExpressionStart(token string) bool {
	return strings.HasPrefix(token, "-") || token == "!" || token == "(" || token == ")" || token == ","
}

func parseFind(tokens []string, scope queryPathResolver, _ bool) (restrictedCommand, error) {
	if err := rejectControlSeparators(tokens); err != nil {
		return restrictedCommand{}, err
	}
	rest := tokens[1:]
	var starts []string
	for len(rest) > 0 && !isFindExpressionStart(rest[0]) {
		starts = append(starts, rest[0])
		rest = rest[1:]
	}
	if len(starts) == 0 {
		starts = []string{"."}
	}
	resolved := make([]string, 0, len(starts))
	for _, start := range starts {
		if start == "" {
			return restrictedCommand{}, invalidArguments("只读模式的 find 起始路径不能为空", nil)
		}
		target, err := scope.resolveExisting(start)
		if err != nil {
			return restrictedCommand{}, queryPathError(start, err)
		}
		resolved = append(resolved, target)
	}
	args := append([]string{}, resolved...)
	depthSeen := map[string]bool{}
	for i := 0; i < len(rest); i++ {
		expression := rest[i]
		switch {
		case findFlagPredicates[expression]:
			args = append(args, expression)
		case expression == "-maxdepth" || expression == "-mindepth":
			if i+1 >= len(rest) {
				return restrictedCommand{}, invalidArguments(fmt.Sprintf("只读模式的 find 缺少 %s 值", expression), nil)
			}
			if depthSeen[expression] {
				return restrictedCommand{}, permissionDenied(fmt.Sprintf("只读模式的 find 重复参数 %q", expression), nil)
			}
			value, err := strconv.Atoi(rest[i+1])
			if err != nil || value < 0 || value > 32 || !isDigits(rest[i+1]) {
				return restrictedCommand{}, invalidArguments(fmt.Sprintf("只读模式的 find %s 必须在 0 到 32 之间", expression), err)
			}
			depthSeen[expression] = true
			args = append(args, expression, strconv.Itoa(value))
			i++
		case expression == "-type":
			if i+1 >= len(rest) || (rest[i+1] != "f" && rest[i+1] != "d" && rest[i+1] != "l") {
				return restrictedCommand{}, invalidArguments("只读模式的 find -type 只能是 f、d 或 l", nil)
			}
			args = append(args, expression, rest[i+1])
			i++
		case findPatternPredicates[expression]:
			if i+1 >= len(rest) || rest[i+1] == "" {
				return restrictedCommand{}, invalidArguments(fmt.Sprintf("只读模式的 find 缺少 %s 模式", expression), nil)
			}
			args = append(args, expression, rest[i+1])
			i++
		case expression == "-size":
			if i+1 >= len(rest) || !findSizePattern.MatchString(rest[i+1]) {
				return restrictedCommand{}, invalidArguments("只读模式的 find -size 格式无效", nil)
			}
			args = append(args, expression, rest[i+1])
			i++
		case findTimePredicates[expression]:
			if i+1 >= len(rest) || !findTimePattern.MatchString(rest[i+1]) {
				return restrictedCommand{}, invalidArguments(fmt.Sprintf("只读模式的 find %s 格式无效", expression), nil)
			}
			args = append(args, expression, rest[i+1])
			i++
		default:
			return restrictedCommand{}, permissionDenied(fmt.Sprintf("只读模式的 find 不支持参数 %q", expression), nil)
		}
	}
	return restrictedCommand{program: "find", args: args, paths: resolved}, nil
}
