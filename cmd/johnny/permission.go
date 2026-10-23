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
