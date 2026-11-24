package subagent

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

type fakeBashClassifier struct {
	mu     sync.Mutex
	calls  []string
	result tool.Classification
	err    error
}

func (f *fakeBashClassifier) Classify(ctx context.Context, command, workDir string) (tool.Classification, error) {
	if err := ctx.Err(); err != nil {
		return tool.Classification{}, err
	}
	f.mu.Lock()
	f.calls = append(f.calls, command)
	result, err := f.result, f.err
	f.mu.Unlock()
	return result, err
}

func (f *fakeBashClassifier) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func TestJEVModeWriteSubagentUsesClassifier(t *testing.T) {
	classifier := &fakeBashClassifier{result: tool.Classification{
		Decision: tool.ClassificationAllow, Reason: "workspace_write", Model: "jev-1.14.0",
	}}
	h := newHarness(t, harnessOptions{
		mode: ModeJEV,
		configure: func(cfg *Config) {
			cfg.Toolbox.Classifier = classifier
		},
	})
	if err := os.MkdirAll(h.path("pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	approvalScript(h, "mkdir pkg/sub")
	if _, err := h.manager.Spawn(context.Background(), writeRequest(h, "建子目录", "pkg")); err != nil {
		t.Fatal(err)
	}
	ended := h.waitEnded(1)[0]
	if ended.Status != agent.StatusCompleted {
		t.Fatalf("JEV 允许后子任务应完成：%+v", ended)
	}
	if calls := classifier.snapshot(); len(calls) != 1 || calls[0] != "mkdir pkg/sub" {
		t.Fatalf("子任务未使用父会话的 JEV 分类器：%#v", calls)
	}
	if _, err := os.Stat(h.path("pkg", "sub")); err != nil {
		t.Fatalf("JEV 允许后命令应执行：%v", err)
	}
}
