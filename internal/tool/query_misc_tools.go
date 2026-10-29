package tool

// tree 属于逃逸口档：-o 会写文件，-R 会在子目录重复运行并写文件，-l 会跟随符号链接，
// --fromfile 会读取任意文件，这些都不在表里。
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

// file 属于逃逸口档：-C 会写 magic 编译文件，-m/-f 读取任意文件，-L 跟随符号链接，
// -p 会改写访问时间，-s 会读取设备文件。
var fileOptions = optionSpec{
	bools:     "bikhnN",
	longBools: []string{"brief", "mime", "mime-type", "mime-encoding", "keep-going", "no-dereference"},
}

func parseFile(tokens []string, scope queryPathResolver, _ bool) (restrictedCommand, error) {
	return parsePathQuery("file", tokens, scope, fileOptions, false)
}
