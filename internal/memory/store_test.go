package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func testStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	home := t.TempDir()
	work := t.TempDir()
	store, err := NewStoreAt(home, work)
	if err != nil {
		t.Fatal(err)
	}
	return store, home, work
}

func projectItem(name, description, body string) Item {
	return Item{
		Name:        name,
		Description: description,
		Type:        TypeProject,
		Scope:       ScopeProject,
		Confidence:  ConfidenceHigh,
		Body:        body,
	}
}

func userItem(name, description, body string) Item {
	return Item{
		Name:        name,
		Description: description,
		Type:        TypePreference,
		Scope:       ScopeUser,
		Confidence:  ConfidenceMedium,
		Body:        body,
	}
}

func TestStoreWriteReadListSearchAndIndex(t *testing.T) {
	store, _, _ := testStore(t)
	ctx := context.Background()
	if _, err := store.Write(ctx, projectItem("go-test", "提交前运行 go test ./...", "提交前运行 `go test ./...`。")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(ctx, userItem("answer-first", "回答先给结论", "先给结论，再展开。")); err != nil {
		t.Fatal(err)
	}
	if store.Project.ID == "" {
		t.Fatal("project-id 为空")
	}

	read, err := store.Read(ScopeProject, "go-test")
	if err != nil {
		t.Fatal(err)
	}
	if read.Project != store.Project.ID || read.Scope != ScopeProject || !strings.Contains(read.Body, "go test") {
		t.Fatalf("项目记忆读取错误：%+v", read)
	}
	if _, err := os.Stat(read.Path); err != nil {
		t.Fatalf("记忆文件不存在：%v", err)
	}
	indexPath, err := store.IndexPath(ScopeProject)
	if err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "`go-test`") {
		t.Fatalf("索引缺少条目：%s", index)
	}

	items, warnings, err := store.List("")
	if err != nil || len(warnings) != 0 || len(items) != 2 {
		t.Fatalf("列表错误：items=%d warnings=%v err=%v", len(items), warnings, err)
	}
	// 项目记忆优先。
	if items[0].Scope != ScopeProject || items[1].Scope != ScopeUser {
		t.Fatalf("排序错误：%+v", items)
	}

	matches, _, err := store.Search("结论", "", SearchLimit)
	if err != nil || len(matches) != 1 || matches[0].Name != "answer-first" {
		t.Fatalf("搜索错误：%+v %v", matches, err)
	}
	matches, _, err = store.Search("go test", "", SearchLimit)
	if err != nil || len(matches) != 1 || matches[0].Name != "go-test" {
		t.Fatalf("正文搜索错误：%+v %v", matches, err)
	}

	text, indexWarning := store.IndexText()
	if indexWarning != "" {
		t.Fatalf("索引警告：%s", indexWarning)
	}
	if !strings.Contains(text, "## 用户记忆索引") || !strings.Contains(text, "## 项目记忆索引") || !strings.Contains(text, "answer-first") {
		t.Fatalf("固定索引错误：\n%s", text)
	}
}

func TestStoreWriteUpdatesAndPreservesCreated(t *testing.T) {
	store, _, _ := testStore(t)
	ctx := context.Background()
	first, err := store.Write(ctx, projectItem("update-me", "旧描述", "旧正文"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Action != ActionCreated {
		t.Fatalf("首次写入动作错误：%s", first.Action)
	}
	second, err := store.Write(ctx, projectItem("update-me", "新描述", "新正文"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Action != ActionUpdated {
		t.Fatalf("更新动作错误：%s", second.Action)
	}
	if !second.Item.Created.Equal(first.Item.Created) {
		t.Fatalf("created 未保留：%v != %v", second.Item.Created, first.Item.Created)
	}
	read, err := store.Read(ScopeProject, "update-me")
	if err != nil || read.Description != "新描述" {
		t.Fatalf("更新未生效：%+v %v", read, err)
	}
}

func TestStoreDeleteRebuildsIndex(t *testing.T) {
	store, _, _ := testStore(t)
	ctx := context.Background()
	if _, err := store.Write(ctx, projectItem("delete-me", "临时", "临时正文")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Delete(ctx, ScopeProject, "delete-me"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(ScopeProject, "delete-me"); !errorsIsNotFound(err) {
		t.Fatalf("删除后应不存在：%v", err)
	}
	indexPath, _ := store.IndexPath(ScopeProject)
	index, _ := os.ReadFile(indexPath)
	if strings.Contains(string(index), "delete-me") {
		t.Fatalf("索引未清理：%s", index)
	}
}

func TestStoreRejectsInvalidAndSecretWrites(t *testing.T) {
	store, _, _ := testStore(t)
	ctx := context.Background()
	if _, err := store.Write(ctx, projectItem("Bad Name", "描述", "正文")); err == nil {
		t.Fatal("无效名称应拒绝")
	}
	if _, err := store.Write(ctx, projectItem("secret", "描述", "JOHNNY_API_KEY=value")); err == nil {
		t.Fatal("密钥应拒绝")
	}
}

func TestStoreIndexBudgetTruncates(t *testing.T) {
	store, _, _ := testStore(t)
	ctx := context.Background()
	for i := 0; i < 60; i++ {
		name := fmt.Sprintf("item-%02d", i)
		item := projectItem(name, strings.Repeat("中", 40), "正文")
		if _, err := store.Write(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	text, warnings := store.IndexText()
	if !strings.Contains(text, "另有") || !strings.Contains(warnings, "已截断") {
		t.Fatalf("索引未按预算截断：\n%s\nwarning=%s", text, warnings)
	}
}

func errorsIsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}
