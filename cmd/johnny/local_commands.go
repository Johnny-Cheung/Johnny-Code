package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

const permissionUsage = `用法：
  /permissions                     查看固定名单和项目规则
  /permissions remove-command 序号  删除一条项目命令前缀
  /permissions remove-path 序号     删除一条项目路径
  /permissions clear               清空项目规则`

// handleLocalCommand 处理不进入模型的本地命令。第二个返回值表示是否已
// 处理；未处理的输入继续作为任务发送。
func handleLocalCommand(line string, rules *tool.ProjectRules) (string, bool) {
	line = strings.TrimSpace(line)
	if line != "/permissions" && !strings.HasPrefix(line, "/permissions ") {
		return "", false
	}
	fields := strings.Fields(line)
	if len(fields) == 1 {
		return tool.PermissionSummary(rules), true
	}
	if rules == nil {
		return "当前模式未加载项目规则；固定名单可用 /permissions 查看。", true
	}
	switch fields[1] {
	case "help":
		return permissionUsage, true
	case "clear":
		if len(fields) != 2 {
			return "用法：/permissions clear", true
		}
		if err := rules.Clear(); err != nil {
			return "清空项目规则失败：" + err.Error(), true
		}
		return "已清空项目规则。", true
	case "remove-command":
		if len(fields) != 3 {
			return "用法：/permissions remove-command 序号", true
		}
		index, err := parsePermissionIndex(fields[2])
		if err != nil {
			return err.Error(), true
		}
		if err := rules.RemovePrefix(index); err != nil {
			return "删除命令前缀失败：" + err.Error(), true
		}
		return fmt.Sprintf("已删除项目命令前缀 %d。", index), true
	case "remove-path":
		if len(fields) != 3 {
			return "用法：/permissions remove-path 序号", true
		}
		index, err := parsePermissionIndex(fields[2])
		if err != nil {
			return err.Error(), true
		}
		if err := rules.RemovePath(index); err != nil {
			return "删除路径失败：" + err.Error(), true
		}
		return fmt.Sprintf("已删除项目路径 %d。", index), true
	default:
		return permissionUsage, true
	}
}

func parsePermissionIndex(value string) (int, error) {
	index, err := strconv.Atoi(value)
	if err != nil || index < 1 {
		return 0, fmt.Errorf("序号必须是正整数：%s", value)
	}
	return index, nil
}
