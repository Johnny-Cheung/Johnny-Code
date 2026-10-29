package tool

// rg 属于逃逸口档：--pre/--pre-glob 会执行外部程序，-z 会调用解压程序，-L 会跟随
// 符号链接，-f/--ignore-file 会读取任意文件。这些选项都不在表里；执行时还会强制
// 加上 --no-config，避免环境变量指向的配置文件注入被拒绝的选项。
//
// -u/--unrestricted 可以重复到三次，但按 ripgrep 语义只会逐级放开 ignore、
// hidden 和 binary 过滤，仍然跳过符号链接；跟随链接只能靠已拒绝的 -L/--follow。
var rgOptions = optionSpec{
	bools:     "nNiswxvlcoqHIFuUaSpbP.0",
	shortInts: map[rune]intRange{'A': {0, 100000, false}, 'B': {0, 100000, false}, 'C': {0, 100000, false}, 'm': {0, 100000, false}, 'd': {0, 32, false}},
	shortStrs: "egtTr",
	longBools: []string{
		"line-number", "no-line-number", "ignore-case", "case-sensitive", "smart-case", "word-regexp",
		"line-regexp", "invert-match", "files-with-matches", "files-without-match", "count", "count-matches",
		"only-matching", "quiet", "with-filename", "no-filename", "fixed-strings", "unrestricted",
		"multiline", "text", "null", "pretty", "byte-offset", "hidden", "no-ignore", "no-ignore-vcs",
		"no-ignore-parent", "no-ignore-dot", "no-messages", "heading", "no-heading", "column", "vimgrep",
		"json", "stats", "trim", "files", "pcre2", "crlf", "one-file-system", "no-config",
	},
	longInts: map[string]intRange{
		"after-context": {0, 100000, false}, "before-context": {0, 100000, false}, "context": {0, 100000, false},
		"max-count": {0, 100000, false}, "max-depth": {0, 32, false}, "max-columns": {0, 100000, false},
	},
	longStrs: []string{"regexp", "glob", "iglob", "type", "type-not", "replace", "color", "sort", "sortr"},
}

func parseRg(tokens []string, scope queryPathResolver, stdin bool) (restrictedCommand, error) {
	opts, err := parseOptions("rg", tokens, rgOptions)
	if err != nil {
		return restrictedCommand{}, err
	}
	operands := opts.operands
	listFiles := opts.seen["files"]
	var pattern []string
	if !listFiles && !opts.seen["e"] && !opts.seen["regexp"] {
		if len(operands) == 0 || operands[0] == "" {
			return restrictedCommand{}, invalidArguments("rg 缺少搜索模式", nil)
		}
		pattern, operands = operands[:1], operands[1:]
	}
	readsStdin := false
	if len(operands) == 0 {
		if stdin && !listFiles {
			readsStdin = true
		} else {
			operands = []string{"."}
		}
	}
	resolved, err := resolveQueryPaths(scope, operands)
	if err != nil {
		return restrictedCommand{}, err
	}
	args := append([]string{"--no-config"}, opts.flags...)
	args = append(args, "--")
	args = append(append(args, pattern...), resolved...)
	return restrictedCommand{program: "rg", args: args, paths: resolved, readsStdin: readsStdin}, nil
}
