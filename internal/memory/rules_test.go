package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRulesAddCreatesAutoSectionAndBackup(t *testing.T) {
	home := t.TempDir()
	rules := NewRules(home)
	ctx := context.Background()
	path, err := rules.Add(ctx, "answer-language-zh", "所有回答使用中文。")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{"## 自动规则", agentsAutoStart, agentsAutoEnd, "`answer-language-zh`", "所有回答使用中文"} {
		if !strings.Contains(content, want) {
			t.Fatalf("缺少 %q：\n%s", want, content)
		}
	}
	if _, err := os.Stat(rules.BackupsDir()); !os.IsNotExist(err) {
		t.Fatalf("首次创建不应有备份目录：%v", err)
	}
	if _, err := rules.Add(ctx, "no-auto-commit", "不要自动 commit。"); err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(filepath.Join(rules.BackupsDir(), "*.md"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("写入前应保留一份备份：%v %v", backups, err)
	}
	if _, err := rules.Add(ctx, "answer-language-zh", "重复规则"); err == nil {
		t.Fatal("重复规则 ID 应拒绝")
	}
}

func TestRulesPreserveManualContent(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".johnny", AgentsFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# 用户规则\n\n## 手动规则\n\n- 手动内容\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rules := NewRules(home)
	if _, err := rules.Add(context.Background(), "new-rule", "新规则"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	content := string(data)
	if !strings.Contains(content, "手动内容") || !strings.Contains(content, "`new-rule`") {
		t.Fatalf("手动内容未保留或规则未写入：\n%s", content)
	}
}

func TestRulesRejectsSecretAndMultiline(t *testing.T) {
	rules := NewRules(t.TempDir())
	ctx := context.Background()
	if _, err := rules.Add(ctx, "secret", "JOHNNY_API_KEY=value"); err == nil {
		t.Fatal("密钥规则应拒绝")
	}
	if _, err := rules.Add(ctx, "valid", "第一行\n第二行"); err != nil {
		t.Fatalf("规则应压缩为一行：%v", err)
	}
}
