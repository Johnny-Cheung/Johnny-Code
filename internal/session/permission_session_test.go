package session

import (
	"encoding/json"
	"testing"
)

func TestRecordPermissionSessionGrants(t *testing.T) {
	recorder := newTestRecorder(t)
	record := PermissionRecord{
		ToolCallID: "call-session",
		Tool:       "bash",
		Target:     "./build.sh --fast 2>&1",
		Decision:   "allow_session",
		Source:     "tui",
		Reason:     "本会话内允许",
		SessionCommands: []PermissionCommandGrant{
			{CWD: "/tmp/project", Prefix: []string{"go", "test"}},
			{CWD: "/tmp/project", Argv: []string{"./build.sh", "--fast"}, Redirects: []string{"2>&1"}},
		},
		SessionPaths: []PermissionPath{{Path: "/tmp/output", Kind: "dir"}},
	}
	if err := recorder.RecordPermission(record); err != nil {
		t.Fatal(err)
	}
	lines := readJSONLines(t, recorder.Path())
	if len(lines) != 2 {
		t.Fatalf("权限记录数量错误：%d", len(lines))
	}
	object := parseObject(t, lines[1])
	if got := stringField(t, object, "decision"); got != "allow_session" {
		t.Fatalf("decision = %q", got)
	}
	var commands []PermissionCommandGrant
	if err := json.Unmarshal(field(t, object, "session_commands"), &commands); err != nil {
		t.Fatalf("decode session_commands: %v", err)
	}
	if len(commands) != 2 || commands[0].CWD != "/tmp/project" {
		t.Fatalf("session_commands cwd 异常：%+v", commands)
	}
	if len(commands[0].Prefix) != 2 || commands[0].Prefix[0] != "go" || commands[0].Prefix[1] != "test" {
		t.Fatalf("session_commands prefix 异常：%+v", commands[0])
	}
	if len(commands[1].Argv) != 2 || commands[1].Argv[1] != "--fast" || len(commands[1].Redirects) != 1 {
		t.Fatalf("session_commands argv/redirects 异常：%+v", commands[1])
	}
	var paths []PermissionPath
	if err := json.Unmarshal(field(t, object, "session_paths"), &paths); err != nil {
		t.Fatalf("decode session_paths: %v", err)
	}
	if len(paths) != 1 || paths[0].Path != "/tmp/output" || paths[0].Kind != "dir" {
		t.Fatalf("session_paths 异常：%+v", paths)
	}
	if _, ok := object["recorded_prefixes"]; ok {
		t.Fatalf("会话授权不应写成项目记录前缀：%s", lines[1])
	}
}
