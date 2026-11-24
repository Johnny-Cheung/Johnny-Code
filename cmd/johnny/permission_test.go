package main

import "testing"

func TestPermissionModeParsing(t *testing.T) {
	for _, value := range []string{"allow", " read-only ", "default", "jev"} {
		if _, err := parsePermissionMode(value); err != nil {
			t.Fatalf("合法权限模式 %q 被拒绝：%v", value, err)
		}
	}
	for _, value := range []string{"", "readonly", "yolo"} {
		if _, err := parsePermissionMode(value); err == nil {
			t.Fatalf("非法权限模式 %q 应失败", value)
		}
	}
	if permissionReadOnly.label() != "只读" || permissionAllow.label() != "放行" || permissionDefault.label() != "默认" || permissionJEV.label() != "JEV" {
		t.Fatal("权限模式展示标签错误")
	}
}

func TestResolvePermissionModeFlags(t *testing.T) {
	for _, test := range []struct {
		value   string
		set     bool
		read    bool
		skip    bool
		jev     bool
		want    permissionMode
		wantErr bool
	}{
		{"", false, false, false, false, permissionDefault, false},
		{"", false, true, false, false, permissionReadOnly, false},
		{"", false, false, true, false, permissionAllow, false},
		{"", false, false, false, true, permissionJEV, false},
		{"", false, true, true, false, "", true},
		{"", false, false, true, true, "", true},
		{"", false, true, false, true, "", true},
		{"read-only", true, true, false, false, permissionReadOnly, false},
		{"read-only", true, false, true, false, "", true},
		{"allow", true, false, true, false, permissionAllow, false},
		{"default", true, false, false, false, permissionDefault, false},
		{"default", true, true, false, false, "", true},
		{"jev", true, false, false, false, permissionJEV, false},
		{"jev", true, false, false, true, permissionJEV, false},
		{"default", true, false, false, true, "", true},
		{"", true, false, false, false, "", true},
		{"bad", true, false, false, false, "", true},
	} {
		got, err := resolvePermissionMode(test.value, test.set, test.read, test.skip, test.jev)
		if test.wantErr {
			if err == nil {
				t.Fatalf("value=%q set=%v read=%v skip=%v jev=%v 应报错", test.value, test.set, test.read, test.skip, test.jev)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Fatalf("value=%q set=%v read=%v skip=%v jev=%v 得到 %q、%v，预期 %q", test.value, test.set, test.read, test.skip, test.jev, got, err, test.want)
		}
	}
}
