package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadOnlyRestrictedPipelines(t *testing.T) {
	readOnly, runtime := readOnlyTest(t)
	if err := os.WriteFile(filepath.Join(runtime.WorkDir, "file.txt"), []byte("hello\nworld\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		command string
		want    string
	}{
		{"ls | head -n 1", "file.txt"},
		{"ls | wc -l", ""},
		{"cat file.txt | wc -l", "2"},
		{"grep -n hello file.txt | head -n 1", "1:hello"},
	} {
		result, err := executeReadOnly(t, readOnly, "bash", map[string]any{"command": test.command})
		if err != nil || (test.want != "" && !strings.Contains(result.Content, test.want)) {
			t.Fatalf("管道 %q 失败：%q %v", test.command, result.Content, err)
		}
	}
}

func TestReadOnlyRestrictedPipelineRejectsUnsupportedForms(t *testing.T) {
	readOnly, runtime := readOnlyTest(t)
	if err := os.WriteFile(filepath.Join(runtime.WorkDir, "file.txt"), []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		"ls | cat",
		"ls | head -n 1 | wc -l",
		"ls | head > out.txt",
		"ls |& head",
		"ls | head; pwd",
		"ls | head && pwd",
		"find . | grep -r hello",
		"ls | head file.txt",
	} {
		_, err := executeReadOnly(t, readOnly, "bash", map[string]any{"command": command})
		requireToolKind(t, err, KindPermissionDenied)
	}
}
