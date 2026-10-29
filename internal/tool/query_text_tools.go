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
		// 归一化成 -e脚本，脚本非空且不可能以 - 开头（地址后必须以 p 结尾）。
		args = append(args, "-e"+operands[0])
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
	// -R/--random-sort 会读 /dev/random 或 --random-source，不在开放范围；
	// --sort=random 是同一能力的别名，必须一起关掉。
	longEnums: map[string][]string{
		"sort": {"general-numeric", "human-numeric", "month", "numeric", "version"},
	},
}

// sort 属于逃逸口档：-o/-T/--compress-program/--files0-from 会写文件或执行
// 程序；即使全部拒绝，sort 在输入超过内存缓冲区时仍会向系统临时目录写
// 临时文件。这是系统临时文件，不是工作目录写入，但不能把 sort 当成“绝不
// 落盘”的纯读命令。
func parseSort(tokens []string, scope queryPathResolver, stdin bool) (restrictedCommand, error) {
	return parseFileReader("sort", tokens, scope, stdin, sortOptions)
}

var uniqOptions = optionSpec{
	bools:     "cdui",
	shortInts: map[rune]intRange{'f': {0, 100000, false}, 's': {0, 100000, false}, 'w': {0, 100000, false}},
	longBools: []string{"count", "repeated", "unique", "ignore-case"},
	longInts:  map[string]intRange{"skip-fields": {0, 100000, false}, "skip-chars": {0, 100000, false}, "check-chars": {0, 100000, false}},
}

// uniq 的第二个文件操作数是输出文件，因此最多接受一个输入文件。BSD 的
// uniq 在第一个操作数之后停止解析选项，`uniq in -c` 会把 -c 当输出文件；
// 因此选项和 -- 必须全部写在第一个操作数之前，不能靠 GNU 的参数重排兜底。
func parseUniq(tokens []string, scope queryPathResolver, stdin bool) (restrictedCommand, error) {
	opts, err := parseOptions("uniq", tokens, uniqOptions)
	if err != nil {
		return restrictedCommand{}, err
	}
	if opts.lateOption || opts.lateTerminator {
		return restrictedCommand{}, permissionDenied("只读模式的 uniq 选项和 -- 必须写在输入文件之前，避免第二个操作数被当成输出文件", nil)
	}
	if len(opts.operands) > 1 {
		return restrictedCommand{}, permissionDenied("只读模式的 uniq 只接受一个输入文件，第二个文件操作数是输出文件", nil)
	}
	return parseFileReader("uniq", tokens, scope, stdin, uniqOptions)
}
