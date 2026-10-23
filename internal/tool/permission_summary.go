package tool

import (
	"fmt"
	"strings"
)

// PermissionSummary 生成 /permissions 的只读文本快照。固定名单来自代码表，
// 项目规则来自当前内存状态；它不是落盘格式。
func PermissionSummary(rules *ProjectRules) string {
	var builder strings.Builder
	builder.WriteString("固定只读白名单：\n")
	for _, line := range fixedQueryCommandLines() {
		builder.WriteString("  " + line + "\n")
	}
	builder.WriteString("\n固定危险后缀黑名单：\n")
	for _, line := range dangerousSuffixLines() {
		builder.WriteString("  " + line + "\n")
	}
	builder.WriteString("\n项目规则：\n")
	if rules == nil {
		builder.WriteString("  （当前模式未加载项目规则）\n")
		return strings.TrimRight(builder.String(), "\n")
	}
	builder.WriteString("  文件：" + rules.FilePath() + "\n")
	if !rules.Trusted() {
		builder.WriteString("  状态：未受信任，本次未应用，也不能新增规则。\n")
	}
	prefixes := rules.CommandPrefixes()
	paths := rules.PathsRules()
	if len(prefixes) == 0 {
		builder.WriteString("  命令前缀：（无）\n")
	} else {
		builder.WriteString("  命令前缀：\n")
		for i, prefix := range prefixes {
			builder.WriteString(fmt.Sprintf("    %d. %s\n", i+1, strings.Join(prefix, " ")))
		}
	}
	if len(paths) == 0 {
		builder.WriteString("  路径：（无）\n")
	} else {
		builder.WriteString("  路径：\n")
		for i, rule := range paths {
			kind := "文件"
			if rule.Kind == PathKindDir {
				kind = "目录"
			}
			builder.WriteString(fmt.Sprintf("    %d. [%s] %s\n", i+1, kind, rule.Path))
		}
	}
	builder.WriteString("  删除命令前缀：/permissions remove-command 序号\n")
	builder.WriteString("  删除路径：/permissions remove-path 序号\n")
	builder.WriteString("  清空项目规则：/permissions clear\n")
	return strings.TrimRight(builder.String(), "\n")
}
