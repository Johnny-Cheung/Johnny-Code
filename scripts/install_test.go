package scripts

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

func runInstalledTUI(t *testing.T, binary, work string, env []string) string {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(master, &pty.Winsize{Rows: 24, Cols: 90}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary)
	cmd.Dir = work
	cmd.Env = append(append([]string(nil), env...), "TERM=xterm-256color")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	kill := func() {
		_ = cmd.Process.Kill()
		select {
		case <-waitDone:
		case <-time.After(time.Second):
		}
	}
	var mu sync.Mutex
	var output bytes.Buffer
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buffer := make([]byte, 4096)
		for {
			n, err := master.Read(buffer)
			if n > 0 {
				mu.Lock()
				_, _ = output.Write(buffer[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	waitFor := func(text string, timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			mu.Lock()
			got := output.String()
			mu.Unlock()
			if strings.Contains(got, text) {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}
	if !waitFor("Enter 发送", 10*time.Second) {
		kill()
		mu.Lock()
		got := output.String()
		mu.Unlock()
		t.Fatalf("安装后的 TUI 未启动：%q", got)
	}
	if _, err := master.Write([]byte("你好\r")); err != nil {
		kill()
		t.Fatal(err)
	}
	if !waitFor("安装成功", 10*time.Second) {
		kill()
		mu.Lock()
		got := output.String()
		mu.Unlock()
		t.Fatalf("安装后的 TUI 未返回模型结果：%q", got)
	}
	if _, err := master.Write([]byte("/exit\r")); err != nil {
		kill()
		t.Fatal(err)
	}
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("安装后的 TUI 退出失败：%v", err)
		}
	case <-time.After(10 * time.Second):
		kill()
		t.Fatal("安装后的 TUI 未退出")
	}
	_ = slave.Close()
	select {
	case <-readDone:
	case <-time.After(time.Second):
	}
	mu.Lock()
	defer mu.Unlock()
	return output.String()
}

func TestInstallUpdateAndCollision(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("安装脚本使用 Bash")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cache, err := exec.Command("go", "env", "GOCACHE", "GOMODCACHE", "GOPATH").Output()
	if err != nil {
		t.Fatal(err)
	}
	cachePaths := strings.Split(strings.TrimSpace(string(cache)), "\n")
	base := t.TempDir()
	source := filepath.Join(base, "source with spaces 'and quotes'")
	bin := filepath.Join(base, "bin with spaces 'and quotes'")
	home := filepath.Join(base, "test-home")
	work := filepath.Join(base, "workspace")
	for _, dir := range []string{source, bin, home, work} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	names := []string{"go.mod", "go.sum", "scripts/install.sh"}
	for _, packageDir := range []string{"cmd/johnny", "internal/config", "internal/model", "internal/tool", "internal/agent", "internal/contextview", "internal/memory", "internal/session", "internal/skill", "internal/tui"} {
		err := filepath.WalkDir(filepath.Join(root, packageDir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			name, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			names = append(names, name)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0700); err != nil {
			t.Fatal(err)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("安装后的程序未读取原位配置")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"安装成功\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	configPath := filepath.Join(source, ".env")
	configData := []byte("JOHNNY_API_KEY=test-key\nJOHNNY_BASE_URL=" + server.URL + "\nJOHNNY_MODEL=test-model\n")
	if err := os.WriteFile(configPath, configData, 0600); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(home, ".bashrc")
	loginProfile := filepath.Join(home, ".profile")
	for _, path := range []string{profile, loginProfile} {
		if err := os.WriteFile(path, []byte("# existing shell settings\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name != "JOHNNY_API_KEY" && name != "JOHNNY_BASE_URL" && name != "JOHNNY_MODEL" && name != "JOHNNY_API" && name != "ZDOTDIR" {
			env = append(env, entry)
		}
	}
	env = append(env, "HOME="+home, "SHELL=/bin/bash", "GOBIN="+bin, "GOCACHE="+cachePaths[0], "GOMODCACHE="+cachePaths[1], "GOPATH="+cachePaths[2], "GOPROXY=off")
	install := func() ([]byte, error) {
		cmd := exec.Command("bash", filepath.Join(source, "scripts/install.sh"))
		cmd.Dir, cmd.Env = work, env
		return cmd.CombinedOutput()
	}
	for i := 0; i < 2; i++ {
		if output, err := install(); err != nil {
			t.Fatalf("安装失败：%v\n%s", err, output)
		}
	}
	profileData, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(profileData), "# Johnny-Code PATH") != 1 || !strings.HasPrefix(string(profileData), "# existing shell settings\n") {
		t.Fatal("重复安装不应重复写入 PATH 或覆盖已有 shell 配置")
	}

	binary := filepath.Join(bin, "johnny")
	tuiOutput := runInstalledTUI(t, binary, work, env)
	if !strings.Contains(tuiOutput, "安装成功") {
		t.Fatalf("跨目录 TUI 调用失败，输出 %q", tuiOutput)
	}
	sessionLogs, err := filepath.Glob(filepath.Join(home, ".johnny", "sessions", "*.jsonl"))
	if err != nil || len(sessionLogs) == 0 {
		t.Fatalf("应在 HOME 下生成会话记录：%v %v", sessionLogs, err)
	}
	if _, err := os.Stat(filepath.Join(work, ".johnny")); !os.IsNotExist(err) {
		t.Fatal("工作目录不应创建日志目录")
	}
	cmd := exec.Command("bash", "--noprofile", "--rcfile", profile, "-ic", "command -v johnny; johnny --version")
	cmd.Dir, cmd.Env = work, env
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), binary) || !strings.Contains(string(output), "johnny v") {
		t.Fatalf("新 shell 未找到安装的程序：%v\n%s", err, output)
	}
	cmd = exec.Command("bash", "--login", "-ic", "command -v johnny; johnny --version")
	cmd.Dir, cmd.Env = work, env
	output, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), binary) || !strings.Contains(string(output), "johnny v") {
		t.Fatalf("登录 shell 未找到安装的程序：%v\n%s", err, output)
	}
	loginData, err := os.ReadFile(loginProfile)
	if err != nil || strings.Count(string(loginData), "# Johnny-Code PATH") != 1 || !strings.HasPrefix(string(loginData), "# existing shell settings\n") {
		t.Fatal("应保留并复用现有登录 shell 配置")
	}

	mainPath := filepath.Join(source, "cmd/johnny/main.go")
	mainData, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := regexp.MustCompile(`version\s*=\s*"[^"]+"`).ReplaceAll(mainData, []byte(`version = "install-test"`))
	if err := os.WriteFile(mainPath, updated, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := install(); err != nil {
		t.Fatalf("更新失败：%v\n%s", err, output)
	}
	cmd = exec.Command(binary, "--version")
	cmd.Env = env
	output, err = cmd.Output()
	if err != nil || !strings.Contains(string(output), "vinstall-test") {
		t.Fatal("更新后版本信息未变化")
	}
	if zsh, err := exec.LookPath("zsh"); err == nil {
		zdir := filepath.Join(base, "custom zsh directory")
		if err := os.MkdirAll(zdir, 0700); err != nil {
			t.Fatal(err)
		}
		zenv := append(env, "SHELL="+zsh, "ZDOTDIR="+zdir)
		cmd := exec.Command("bash", filepath.Join(source, "scripts/install.sh"))
		cmd.Dir, cmd.Env = work, zenv
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("自定义 ZDOTDIR 安装失败：%v\n%s", err, output)
		}
		cmd = exec.Command(zsh, "-ic", "command -v johnny; johnny --version")
		cmd.Dir, cmd.Env = work, zenv
		output, err = cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(output), binary) || !strings.Contains(string(output), "vinstall-test") {
			t.Fatalf("自定义 ZDOTDIR 的新 shell 不可用：%v\n%s", err, output)
		}
	}
	before, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainPath, []byte("invalid Go source"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := install(); err == nil {
		t.Fatal("构建失败时应停止安装")
	}
	after, err := os.ReadFile(binary)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("构建失败不应损坏已安装的程序")
	}
	if data, err := os.ReadFile(configPath); err != nil || string(data) != string(configData) {
		t.Fatal("安装或更新不应修改已有配置")
	}
	if data, err := os.ReadFile(profile); err != nil || string(data) != string(profileData) {
		t.Fatal("更新不应重复修改 PATH")
	}
	if data, err := os.ReadFile(loginProfile); err != nil || string(data) != string(loginData) {
		t.Fatal("更新不应重复修改登录 shell 的 PATH")
	}

	foreignBin := filepath.Join(base, "foreign-bin")
	if err := os.MkdirAll(foreignBin, 0700); err != nil {
		t.Fatal(err)
	}
	foreignPath := filepath.Join(foreignBin, "johnny")
	foreignData := []byte("other program")
	if err := os.WriteFile(foreignPath, foreignData, 0700); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("bash", filepath.Join(source, "scripts/install.sh"))
	cmd.Dir, cmd.Env = work, append(env, "GOBIN="+foreignBin)
	if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "未覆盖") {
		t.Fatalf("应拒绝覆盖其他程序：%v\n%s", err, output)
	}
	if data, err := os.ReadFile(foreignPath); err != nil || string(data) != string(foreignData) {
		t.Fatal("其他程序被修改")
	}
}
