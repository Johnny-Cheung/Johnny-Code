package tool

// tree 属于逃逸口档：-o 会写文件，-R 会在子目录重复运行并写文件，-l 会跟随符号链接，
// --fromfile/--gitfile/--infofile 等会读取任意文件，这些都不在表里。-A 只是 ANSI
// 线条绘制选项，不写文件也不跟随链接，可以保留。
var treeOptions = optionSpec{
	bools:     "adfiphusDFNQnCvrxtJA",
	shortInts: map[rune]intRange{'L': {1, 32, false}},
	shortStrs: "IP",
	longBools: []string{"dirsfirst", "noreport", "gitignore", "prune", "du", "inodes", "device"},
	longStrs:  []string{"charset"},
}

func parseTree(tokens []string, scope queryPathResolver, _ bool) (restrictedCommand, error) {
	return parsePathQuery("tree", tokens, scope, treeOptions, true)
}

// file 属于逃逸口档：-C 会写 magic 编译文件，-m/-M/-f/-e 读取任意文件，-L 跟随
// 符号链接，-p 会改写访问时间，-s 会读取设备文件，-S 会关闭 file 自身的 sandbox。
// MAGIC/MAGIC_CONTINUE 等环境变量由 restrictedEnvironment 统一剥离，不能从环境
// 指向自定义 magic 文件。
var fileOptions = optionSpec{
	bools:     "bikhnN",
	longBools: []string{"brief", "mime", "mime-type", "mime-encoding", "keep-going", "no-dereference"},
}

func parseFile(tokens []string, scope queryPathResolver, _ bool) (restrictedCommand, error) {
	return parsePathQuery("file", tokens, scope, fileOptions, false)
}
