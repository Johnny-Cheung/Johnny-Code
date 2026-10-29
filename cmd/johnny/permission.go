package main

import (
	"fmt"
	"strings"
)

type permissionMode string

const (
	permissionDefault  permissionMode = "default"
	permissionAllow    permissionMode = "allow"
	permissionReadOnly permissionMode = "read-only"
)

func parsePermissionMode(value string) (permissionMode, error) {
	switch strings.TrimSpace(value) {
	case string(permissionDefault):
		return permissionDefault, nil
	case string(permissionAllow):
		return permissionAllow, nil
	case string(permissionReadOnly):
		return permissionReadOnly, nil
	default:
		return "", fmt.Errorf("--permission-mode 只能是 %q、%q 或 %q", permissionDefault, permissionReadOnly, permissionAllow)
	}
}

// resolvePermissionMode 把短选项映射到权限模式。不传任何权限参数时使用
// 默认模式；--read 对应只读，--skip 对应放行。长写法 --permission-mode
// 继续保留，与短选项同时出现时必须一致。
func resolvePermissionMode(value string, set, readOnly, skip bool) (permissionMode, error) {
	if readOnly && skip {
		return "", fmt.Errorf("--read 和 --skip 不能同时使用")
	}
	shortcut := permissionMode("")
	switch {
	case readOnly:
		shortcut = permissionReadOnly
	case skip:
		shortcut = permissionAllow
	}
	explicit := permissionMode("")
	if set {
		parsed, err := parsePermissionMode(value)
		if err != nil {
			return "", err
		}
		explicit = parsed
	}
	if explicit != "" && shortcut != "" && explicit != shortcut {
		return "", fmt.Errorf("--permission-mode %q 与 --read/--skip 指定的模式冲突", value)
	}
	if explicit != "" {
		return explicit, nil
	}
	if shortcut != "" {
		return shortcut, nil
	}
	return permissionDefault, nil
}

func (m permissionMode) label() string {
	switch m {
	case permissionReadOnly:
		return "只读"
	case permissionDefault:
		return "默认"
	default:
		return "放行"
	}
}
