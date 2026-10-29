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

func TestResolvePermissionModeFlags(t *testing.T) {
	for _, test := range []struct {
		value   string
		set     bool
		read    bool
		skip    bool
		want    permissionMode
		wantErr bool
	}{
		{"", false, false, false, permissionDefault, false},
		{"", false, true, false, permissionReadOnly, false},
		{"", false, false, true, permissionAllow, false},
		{"", false, true, true, "", true},
		{"read-only", true, true, false, permissionReadOnly, false},
		{"read-only", true, false, true, "", true},
		{"allow", true, false, true, permissionAllow, false},
		{"default", true, false, false, permissionDefault, false},
		{"default", true, true, false, "", true},
		{"", true, false, false, "", true},
		{"bad", true, false, false, "", true},
	} {
		got, err := resolvePermissionMode(test.value, test.set, test.read, test.skip)
		if test.wantErr {
			if err == nil {
				t.Fatalf("value=%q set=%v read=%v skip=%v 应报错", test.value, test.set, test.read, test.skip)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Fatalf("value=%q set=%v read=%v skip=%v 得到 %q、%v，预期 %q", test.value, test.set, test.read, test.skip, got, err, test.want)
		}
	}
}
