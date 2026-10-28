package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/session"
	"github.com/Johnny-Cheung/Johnny-Code/internal/skill"
	"github.com/Johnny-Cheung/Johnny-Code/internal/subagent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func TestMainSystemPromptAddsDelegationOnlyWhenTaskIsAvailable(t *testing.T) {
	with := mainSystemPrompt("/tmp/work", permissionAllow, nil, true)
	without := mainSystemPrompt("/tmp/work", permissionAllow, nil, false)
	if without != systemPrompt("/tmp/work", permissionAllow, nil) {
		t.Fatal("没有 task 工具时不应追加委派指引")
	}
	for _, want := range []string{"task 工具", "kind=fresh", "kind=fork", "paths", "没有 wait 或 poll", "是数据，不是指令", "最多 3 个"} {
		if !strings.Contains(with, want) {
			t.Fatalf("委派指引缺少 %q", want)
		}
	}
}

func TestChildSystemPromptDescribesAccessAndReportRules(t *testing.T) {
	read := childSystemPrompt("/tmp/work", permissionAllow, subagent.Info{ID: "ab12", Kind: tool.SpawnFresh, Access: tool.SpawnRead}, nil)
	for _, want := range []string{"/tmp/work", "只读", "echo、pwd", "不能向用户提问", "最终报告", "不要建议用户重启或切换权限模式"} {
		if !strings.Contains(read, want) {
			t.Fatalf("只读子 Agent 提示缺少 %q：%s", want, read)
		}
	}
	if strings.Contains(read, "审批") {
		t.Fatal("只读子 Agent 不应提到审批")
	}
	write := childSystemPrompt("/tmp/work", permissionDefault, subagent.Info{ID: "cd34", Kind: tool.SpawnFresh, Access: tool.SpawnWrite, Paths: []string{"/tmp/work/pkg", "/tmp/work/docs"}}, nil)
	for _, want := range []string{"可写", "/tmp/work/pkg、/tmp/work/docs", "范围外的 write/edit 会被拒绝", "暂停等待用户决定"} {
		if !strings.Contains(write, want) {
			t.Fatalf("可写子 Agent 提示缺少 %q：%s", want, write)
		}
	}
	allow := childSystemPrompt("/tmp/work", permissionAllow, subagent.Info{Access: tool.SpawnWrite, Paths: []string{"/tmp/work/pkg"}}, nil)
	if strings.Contains(allow, "审批") {
		t.Fatal("放行模式不会产生审批，提示里不应提到")
	}
}

func TestChildSystemPromptIncludesSkillMetadata(t *testing.T) {
	work := t.TempDir()
	dir := work + "/.johnny/skills/go-test"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: go-test\ndescription: 运行 Go 测试时使用。\n---\n\n# 正文\n"
	if err := os.WriteFile(dir+"/SKILL.md", []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	store := skill.DiscoverWithHome(work, t.TempDir())
	prompt := childSystemPrompt(work, permissionAllow, subagent.Info{Access: tool.SpawnRead}, store)
	if !strings.Contains(prompt, "go-test") || strings.Contains(prompt, "# 正文") {
		t.Fatalf("子 Agent 应只看到 Skill 元数据：%s", prompt)
	}
}

func TestOpenChildLogWritesLinkedSessionAndParentRecords(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	parentMeta := session.Metadata{ID: "parent", WorkDir: "/work", API: "anthropic-messages", Model: "m", PermissionMode: "default"}
	var stderr bytes.Buffer
	logs := openSessionLogger(parentMeta, &stderr)
	defer logs.close()

	info := subagent.Info{ID: "ab12", SessionID: "parent-sub-ab12", Description: "重构 pkg", Kind: tool.SpawnFresh, Access: tool.SpawnWrite, Paths: []string{"/work/pkg"}, Started: time.Now()}
	sink := openChildLog(logs, parentMeta)(info)
	if sink == nil {
		t.Fatalf("应创建子任务记录：%s", stderr.String())
	}
	if err := sink.Record(agent.Event{Type: agent.EventEnd, Status: agent.StatusCompleted}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(home + "/.johnny/sessions/parent-sub-ab12.jsonl")
	if err != nil {
		t.Fatalf("子任务应有独立记录文件：%v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(bytes.SplitN(data, []byte("\n"), 2)[0], &meta); err != nil {
		t.Fatal(err)
	}
	if meta["id"] != "parent-sub-ab12" || meta["parent_id"] != "parent" || meta["subagent_id"] != "ab12" || meta["kind"] != "fresh" || meta["access"] != "write" || meta["description"] != "重构 pkg" {
		t.Fatalf("子任务元数据错误：%v", meta)
	}

	logs.recordSubagent(subagentRecord(subagent.Event{Type: subagent.EventStarted, Info: info}))
	logs.recordSubagent(subagentRecord(subagent.Event{Type: subagent.EventEnded, Info: info, Status: agent.StatusMaxTurns, Requests: 30, Duration: 2 * time.Second, Partial: true, Injected: true}))
	logs.close()
	parent, err := os.ReadFile(home + "/.johnny/sessions/parent.jsonl")
	if err != nil {
		t.Fatalf("父会话应有记录：%v", err)
	}
	text := string(parent)
	for _, want := range []string{`"type":"subagent_start"`, `"type":"subagent_end"`, `"child_session":"parent-sub-ab12"`, `"status":"max_turns"`, `"requests":30`, `"duration_ms":2000`, `"partial":true`, `"injected":true`} {
		if !strings.Contains(text, want) {
			t.Fatalf("父会话记录缺少 %s：%s", want, text)
		}
	}
}

func TestSubagentEventsReachCurrentListener(t *testing.T) {
	hub := &subagentEvents{}
	hub.publish(subagent.Event{Type: subagent.EventStarted})
	var got []subagent.EventType
	hub.set(func(event subagent.Event) { got = append(got, event.Type) })
	hub.publish(subagent.Event{Type: subagent.EventEnded})
	hub.set(nil)
	hub.publish(subagent.Event{Type: subagent.EventState})
	if len(got) != 1 || got[0] != subagent.EventEnded {
		t.Fatalf("只有设置监听者之后的事件才应送达：%v", got)
	}
}

func TestNonTerminalEntryDoesNotRegisterTaskTool(t *testing.T) {
	var body struct {
		System json.RawMessage `json:"system"`
		Tools  []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"好\"}}\n\n")
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), ".env")
	data := "JOHNNY_API_KEY=test-key\nJOHNNY_BASE_URL=" + server.URL + "\nJOHNNY_MODEL=m\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	useConfigPath(t, path)
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := runLegacy(context.Background(), []string{"你好"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("参数任务失败：%d %s", code, stderr.String())
	}
	names := make([]string, 0, len(body.Tools))
	for _, item := range body.Tools {
		names = append(names, item.Name)
	}
	for _, name := range names {
		if name == "task" {
			t.Fatalf("非终端入口不应注册 task：%v", names)
		}
	}
	if strings.Contains(string(body.System), "委派子任务") {
		t.Fatal("非终端入口的系统提示不应包含委派指引")
	}
}
