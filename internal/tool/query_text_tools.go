package tool

import (
	"fmt"
	"regexp"
)

// sed 属于逃逸口档：w、e、r 命令和 s///w、s///e、-i 会写文件或执行程序。
// 这里只开放“行范围或正则范围加 p”的打印形式，其余脚本一律拒绝。
var sedPrintScript = regexp.MustCompile(`^(?:[0-9]+|\$|/[^/\\]*/)(?:,(?:[0-9]+|\$|/[^/\\]*/))?p$`)

var sedOptions = optionSpec{
	bools:     "nEr",
	shortStrs: "e",
	longBools: []string{"quiet", "silent", "regexp-extended"},
	longStrs:  []string{"expression"},
}

func parseSed(tokens []string, scope queryPathResolver, stdin bool) (restrictedCommand, error) {
	opts, err := parseOptions("sed", tokens, sedOptions)
	if err != nil {
		return restrictedCommand{}, err
	}
	operands := opts.operands
	scripts := append(append([]string{}, opts.values["e"]...), opts.values["expression"]...)
	for _, script := range scripts {
		if !sedPrintScript.MatchString(script) {
			return restrictedCommand{}, permissionDenied(fmt.Sprintf("只读模式的 sed 只支持行范围加 p 的打印脚本，不支持 %q", script), nil)
		}
	}
	args := append([]string{}, opts.flags...)
	if len(scripts) == 0 {
		if len(operands) == 0 || !sedPrintScript.MatchString(operands[0]) {
			return restrictedCommand{}, permissionDenied("只读模式的 sed 只支持行范围加 p 的打印脚本，例如 sed -n '1,20p' 文件", nil)
		}
		args = append(args, "-e", operands[0])
		operands = operands[1:]
	}
	if len(operands) == 0 {
		if !stdin {
			return restrictedCommand{}, invalidArguments("只读模式的 sed 至少需要一个路径", nil)
		}
		return restrictedCommand{program: "sed", args: args, readsStdin: true}, nil
	}
	if err := rejectStdinPaths(operands); err != nil {
		return restrictedCommand{}, err
	}
	resolved, err := resolveRegularQueryPaths(scope, operands)
	if err != nil {
		return restrictedCommand{}, err
	}
	return restrictedCommand{program: "sed", args: appendFlagsAndPaths(args, resolved), paths: resolved}, nil
}

var sortOptions = optionSpec{
	bools:     "rnufbdghVcCsMzm",
	shortStrs: "kt",
	longBools: []string{
		"reverse", "numeric-sort", "unique", "ignore-case", "ignore-leading-blanks", "dictionary-order",
		"general-numeric-sort", "human-numeric-sort", "version-sort", "stable", "month-sort", "check",
		"zero-terminated", "merge",
	},
	longStrs: []string{"key", "field-separator", "sort"},
}

func parseSort(tokens []string, scope queryPathResolver, stdin bool) (restrictedCommand, error) {
	return parseFileReader("sort", tokens, scope, stdin, sortOptions)
}

var uniqOptions = optionSpec{
	bools:     "cdui",
	shortInts: map[rune]intRange{'f': {0, 100000, false}, 's': {0, 100000, false}, 'w': {0, 100000, false}},
	longBools: []string{"count", "repeated", "unique", "ignore-case"},
	longInts:  map[string]intRange{"skip-fields": {0, 100000, false}, "skip-chars": {0, 100000, false}, "check-chars": {0, 100000, false}},
}

// uniq 的第二个文件操作数是输出文件，因此最多接受一个输入文件。
func parseUniq(tokens []string, scope queryPathResolver, stdin bool) (restrictedCommand, error) {
	opts, err := parseOptions("uniq", tokens, uniqOptions)
	if err != nil {
		return restrictedCommand{}, err
	}
	if len(opts.operands) > 1 {
		return restrictedCommand{}, permissionDenied("只读模式的 uniq 只接受一个输入文件，第二个文件操作数是输出文件", nil)
	}
	return parseFileReader("uniq", tokens, scope, stdin, uniqOptions)
}
