package tool

import "runtime"

// 纯读档：这些程序本身没有写文件或执行其他程序的参数，选项表因此可以放宽；
// 表外的选项（会跟随符号链接、从文件读取参数或一直不返回的形式）仍然拒绝。

var lsOptions = optionSpec{
	bools: "aAlh1FdRtSrsikmnpqxCGfUcu",
	longBools: []string{
		"all", "almost-all", "human-readable", "group-directories-first", "reverse",
		"recursive", "directory", "classify", "size", "inode", "color",
	},
	longStrs: []string{"color"},
}

var grepOptions = optionSpec{
	bools:     "nirEFvlLcwxoHhsqaIzPbTUZ",
	shortInts: map[rune]intRange{'A': {0, 100000, false}, 'B': {0, 100000, false}, 'C': {0, 100000, false}, 'm': {0, 100000, false}},
	shortStrs: "e",
	numeric:   true,
	longBools: []string{
		"line-number", "ignore-case", "invert-match", "files-with-matches", "files-without-match",
		"count", "word-regexp", "line-regexp", "only-matching", "no-filename", "with-filename",
		"recursive", "extended-regexp", "fixed-strings", "perl-regexp", "basic-regexp", "quiet",
		"silent", "no-messages", "text", "null", "initial-tab", "byte-offset", "color", "colour",
		// --context 的取值是可选的：裸 --context 等价于 -C 2，只有 = 形式才
		// 消费数字，避免把后面的 pattern/路径误当取值。
		"context",
	},
	longInts: map[string]intRange{
		"max-count": {0, 100000, false}, "after-context": {0, 100000, false},
		"before-context": {0, 100000, false}, "context": {0, 100000, false},
	},
	longStrs: []string{"include", "exclude", "exclude-dir", "regexp", "color", "colour", "binary-files"},
}

var catOptions = optionSpec{
	bools: "benstvAETu",
	longBools: []string{
		"number", "number-nonblank", "squeeze-blank", "show-all", "show-ends", "show-tabs", "show-nonprinting",
	},
}

var headOptions = optionSpec{
	bools:     "qv",
	shortInts: map[rune]intRange{'n': {0, 1 << 30, false}, 'c': {0, 1 << 30, false}},
	numeric:   true,
	longBools: []string{"quiet", "silent", "verbose"},
	longInts:  map[string]intRange{"lines": {0, 1 << 30, false}, "bytes": {0, 1 << 30, false}},
}

var tailOptions = optionSpec{
	bools:     "qv",
	shortInts: map[rune]intRange{'n': {0, 1 << 30, true}, 'c': {0, 1 << 30, true}},
	numeric:   true,
	longBools: []string{"quiet", "silent", "verbose"},
	longInts:  map[string]intRange{"lines": {0, 1 << 30, true}, "bytes": {0, 1 << 30, true}},
}

var wcOptions = optionSpec{
	bools:     "lwcmL",
	longBools: []string{"lines", "words", "bytes", "chars", "max-line-length"},
}

// stat 的短选项在 GNU 与 BSD 上含义不同：GNU 的 -f 是“文件系统状态”布尔量、
// -c 才是格式，-t 是 terse；BSD 的 -f/-t 都带取值，-c 不存在。选项表按运行
// 平台封闭选择，避免把某个平台的取值 token 在另一个平台当成未解析的路径
// 操作数。无论哪边，带值选项在执行前都会归一化成粘连形式。
func statOptionsFor(goos string) optionSpec {
	switch goos {
	case "linux":
		return optionSpec{
			bools:     "ft",
			shortStrs: "c",
			longBools: []string{"terse"},
			longStrs:  []string{"format", "printf"},
		}
	case "darwin", "freebsd", "netbsd", "openbsd", "dragonfly":
		return optionSpec{
			bools:     "xs",
			shortStrs: "ft",
		}
	default:
		// 未知平台只保留无取值选项为空的保守表，至少可以 stat 路径本身。
		return optionSpec{}
	}
}

var statOptions = statOptionsFor(runtime.GOOS)

var duOptions = optionSpec{
	bools:     "achkmsxb",
	shortInts: map[rune]intRange{'d': {0, 32, false}},
	longBools: []string{
		"all", "total", "human-readable", "summarize", "si", "apparent-size", "bytes", "one-file-system",
	},
	longInts: map[string]intRange{"max-depth": {0, 32, false}},
	longStrs: []string{"exclude"},
}

var diffOptions = optionSpec{
	bools:     "abBcEiNqrtuwsy",
	shortInts: map[rune]intRange{'U': {0, 32, false}, 'C': {0, 32, false}},
	longBools: []string{
		"brief", "recursive", "unified", "context", "ignore-case", "ignore-all-space",
		"ignore-space-change", "ignore-blank-lines", "new-file", "text", "side-by-side",
		"report-identical-files", "minimal", "ignore-trailing-space",
	},
	longInts: map[string]intRange{"unified": {0, 32, false}, "context": {0, 32, false}},
}

var realpathOptions = optionSpec{bools: "emsLPqz"}

func parseLs(tokens []string, scope queryPathResolver, _ bool) (restrictedCommand, error) {
	opts, err := parseOptions("ls", tokens, lsOptions)
	if err != nil {
		return restrictedCommand{}, err
	}
	paths := opts.operands
	if len(paths) == 0 {
		paths = []string{"."}
	}
	resolved, err := resolveQueryPaths(scope, paths)
	if err != nil {
		return restrictedCommand{}, err
	}
	return restrictedCommand{program: "ls", args: appendFlagsAndPaths(opts.flags, resolved), paths: resolved}, nil
}

func parseGrep(tokens []string, scope queryPathResolver, stdin bool) (restrictedCommand, error) {
	opts, err := parseOptions("grep", tokens, grepOptions)
	if err != nil {
		return restrictedCommand{}, err
	}
	operands := opts.operands
	var pattern []string
	if !opts.seen["e"] && !opts.seen["regexp"] {
		if len(operands) == 0 || operands[0] == "" {
			return restrictedCommand{}, invalidArguments("只读模式的 grep 缺少搜索模式", nil)
		}
		pattern, operands = operands[:1], operands[1:]
	}
	readsStdin := false
	if len(operands) == 0 {
		switch {
		case opts.seen["r"] || opts.seen["recursive"]:
			operands = []string{"."}
		case stdin:
			readsStdin = true
		default:
			return restrictedCommand{}, invalidArguments("只读模式的 grep 至少需要一个路径", nil)
		}
	}
	resolved, err := resolveQueryPaths(scope, operands)
	if err != nil {
		return restrictedCommand{}, err
	}
	args := append(append([]string{}, opts.flags...), "--")
	args = append(append(args, pattern...), resolved...)
	return restrictedCommand{program: "grep", args: args, paths: resolved, readsStdin: readsStdin}, nil
}

// parseFileReader 处理 cat/head/tail/wc 这类“读取一个以上普通文件，管道中读标准输入”的程序。
func parseFileReader(program string, tokens []string, scope queryPathResolver, stdin bool, spec optionSpec) (restrictedCommand, error) {
	opts, err := parseOptions(program, tokens, spec)
	if err != nil {
		return restrictedCommand{}, err
	}
	if len(opts.operands) == 0 {
		if stdin {
			return restrictedCommand{program: program, args: opts.flags, readsStdin: true}, nil
		}
		return restrictedCommand{}, invalidArguments("只读模式的 "+program+" 至少需要一个路径", nil)
	}
	if err := rejectStdinPaths(opts.operands); err != nil {
		return restrictedCommand{}, err
	}
	resolved, err := resolveRegularQueryPaths(scope, opts.operands)
	if err != nil {
		return restrictedCommand{}, err
	}
	return restrictedCommand{program: program, args: appendFlagsAndPaths(opts.flags, resolved), paths: resolved}, nil
}

func parseCat(tokens []string, scope queryPathResolver, stdin bool) (restrictedCommand, error) {
	return parseFileReader("cat", tokens, scope, stdin, catOptions)
}

func parseHead(tokens []string, scope queryPathResolver, stdin bool) (restrictedCommand, error) {
	return parseFileReader("head", tokens, scope, stdin, headOptions)
}

func parseTail(tokens []string, scope queryPathResolver, stdin bool) (restrictedCommand, error) {
	return parseFileReader("tail", tokens, scope, stdin, tailOptions)
}

func parseWc(tokens []string, scope queryPathResolver, stdin bool) (restrictedCommand, error) {
	return parseFileReader("wc", tokens, scope, stdin, wcOptions)
}

func parseStat(tokens []string, scope queryPathResolver, _ bool) (restrictedCommand, error) {
	return parsePathQuery("stat", tokens, scope, statOptions, false)
}

func parseDu(tokens []string, scope queryPathResolver, _ bool) (restrictedCommand, error) {
	return parsePathQuery("du", tokens, scope, duOptions, true)
}

func parseRealpath(tokens []string, scope queryPathResolver, _ bool) (restrictedCommand, error) {
	return parsePathQuery("realpath", tokens, scope, realpathOptions, false)
}

// parsePathQuery 处理接受任意已存在路径的程序；defaultDot 为 true 时无操作数等于当前目录。
func parsePathQuery(program string, tokens []string, scope queryPathResolver, spec optionSpec, defaultDot bool) (restrictedCommand, error) {
	opts, err := parseOptions(program, tokens, spec)
	if err != nil {
		return restrictedCommand{}, err
	}
	paths := opts.operands
	if len(paths) == 0 {
		if !defaultDot {
			return restrictedCommand{}, invalidArguments("只读模式的 "+program+" 至少需要一个路径", nil)
		}
		paths = []string{"."}
	}
	if err := rejectStdinPaths(paths); err != nil {
		return restrictedCommand{}, err
	}
	resolved, err := resolveQueryPaths(scope, paths)
	if err != nil {
		return restrictedCommand{}, err
	}
	return restrictedCommand{program: program, args: appendFlagsAndPaths(opts.flags, resolved), paths: resolved}, nil
}

func parseDiff(tokens []string, scope queryPathResolver, _ bool) (restrictedCommand, error) {
	opts, err := parseOptions("diff", tokens, diffOptions)
	if err != nil {
		return restrictedCommand{}, err
	}
	if len(opts.operands) != 2 {
		return restrictedCommand{}, invalidArguments("只读模式的 diff 需要恰好两个路径", nil)
	}
	if err := rejectStdinPaths(opts.operands); err != nil {
		return restrictedCommand{}, err
	}
	recursive := opts.seen["r"] || opts.seen["recursive"]
	resolved, err := resolveDiffPaths(scope, opts.operands, recursive)
	if err != nil {
		return restrictedCommand{}, err
	}
	flags := opts.flags
	if recursive {
		// GNU diff 默认会跟随目录里遇到的符号链接，可能读到工作目录外；
		// 强制 --no-dereference（不支持该选项的实现会直接报错，失败关闭）。
		flags = append(append([]string{}, flags...), "--no-dereference")
	}
	return restrictedCommand{program: "diff", args: appendFlagsAndPaths(flags, resolved), paths: resolved}, nil
}

// parseLiteral 处理只对字符串求值、不访问文件系统的程序（echo、basename、dirname）。
func parseLiteral(program string) func([]string, queryPathResolver, bool) (restrictedCommand, error) {
	return func(tokens []string, _ queryPathResolver, _ bool) (restrictedCommand, error) {
		if err := rejectControlSeparators(tokens); err != nil {
			return restrictedCommand{}, err
		}
		return restrictedCommand{program: program, args: append([]string(nil), tokens[1:]...)}, nil
	}
}
