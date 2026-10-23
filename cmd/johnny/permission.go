package main

import (
	"fmt"
	"strings"
)

type permissionMode string

const (
	permissionAllow    permissionMode = "allow"
	permissionReadOnly permissionMode = "read-only"
)

func parsePermissionMode(value string) (permissionMode, error) {
	switch strings.TrimSpace(value) {
	case string(permissionAllow):
		return permissionAllow, nil
	case string(permissionReadOnly):
		return permissionReadOnly, nil
	default:
		return "", fmt.Errorf("--permission-mode 只能是 %q 或 %q", permissionReadOnly, permissionAllow)
	}
}

func (m permissionMode) label() string {
	if m == permissionReadOnly {
		return "只读"
	}
	return "放行"
}
