package main

import "testing"

func TestPermissionModeParsing(t *testing.T) {
	for _, value := range []string{"allow", " read-only ", "default"} {
		if _, err := parsePermissionMode(value); err != nil {
			t.Fatalf("合法权限模式 %q 被拒绝：%v", value, err)
		}
	}
	for _, value := range []string{"", "readonly", "yolo"} {
		if _, err := parsePermissionMode(value); err == nil {
			t.Fatalf("非法权限模式 %q 应失败", value)
		}
	}
	if permissionReadOnly.label() != "只读" || permissionAllow.label() != "放行" || permissionDefault.label() != "默认" {
		t.Fatal("权限模式展示标签错误")
	}
}
