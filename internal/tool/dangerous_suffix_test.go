package tool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDangerousSuffixTokenMatching(t *testing.T) {
	cases := []struct {
		name    string
		program string
		tokens  []string
		hit     string
		ok      bool
	}{
		// make：短选项粘连和短选项簇。
		{"make -f attached", "make", []string{"make", "-fevil.mk"}, "-f", true},
		{"make -C attached", "make", []string{"make", "-Csub"}, "-C", true},
		{"make -I attached", "make", []string{"make", "-Iinc"}, "-I", true},
		{"make -sf attached", "make", []string{"make", "-sfevil.mk"}, "-f", true},
		{"make -nC cluster", "make", []string{"make", "-nCsub"}, "-C", true},
		{"make -f= attached", "make", []string{"make", "-f=evil.mk"}, "-f", true},
		{"make -W value", "make", []string{"make", "-Wf"}, "", false},
		{"make -o value", "make", []string{"make", "-oFile"}, "", false},
		{"make -j value", "make", []string{"make", "-j4"}, "", false},
		{"program path", "/usr/bin/make", []string{"/usr/bin/make", "-sfevil.mk"}, "-f", true},

		// GNU/git 风格长选项缩写与 = 取值。
		{"make --fil=", "make", []string{"make", "--fil=evil.mk"}, "--file", true},
		{"make --f=", "make", []string{"make", "--f=evil.mk"}, "--file", true},
		{"make --dir=", "make", []string{"make", "--dir=sub"}, "--directory", true},
		{"make --inc=", "make", []string{"make", "--inc=."}, "--include-dir", true},
		{"make --file separate", "make", []string{"make", "--file", "evil.mk"}, "--file", true},
		{"git --out=", "git", []string{"git", "log", "--out=file"}, "--output", true},
		{"git --out separate", "git", []string{"git", "log", "--out", "file"}, "--output", true},
		{"git --pag", "git", []string{"git", "log", "--pag"}, "--paginate", true},
		{"git --no-i", "git", []string{"git", "log", "--no-i"}, "--no-index", true},
		{"git --git-d=", "git", []string{"git", "--git-d=/tmp/repo", "status"}, "--git-dir", true},
		{"git -c attached", "git", []string{"git", "-ccore.editor=x", "log"}, "-c", true},
		{"git -pc cluster", "git", []string{"git", "-pccore.editor=x", "log"}, "-c", true},

		// 包管理器：nopt 等实现接受长选项缩写。
		{"npm --prefi=", "npm", []string{"npm", "--prefi=/tmp/x", "run", "build"}, "--prefix", true},
		{"npm ambiguous --pref=", "npm", []string{"npm", "--pref=/tmp/x", "run", "build"}, "--prefix", true},
		{"npm --script-s=", "npm", []string{"npm", "run", "build", "--script-s=/bin/sh"}, "--script-shell", true},
		{"npm --shel=", "npm", []string{"npm", "run", "build", "--shel=/bin/sh"}, "--shell", true},
		{"npm --cw=", "npm", []string{"npm", "run", "build", "--cw=/tmp"}, "--cwd", true},
		{"npm --di=", "npm", []string{"npm", "run", "build", "--di=/tmp"}, "--dir", true},
		{"yarn --pref=", "yarn", []string{"yarn", "--pref=/tmp/x", "run", "build"}, "--prefix", true},
		{"pnpm --script-s=", "pnpm", []string{"pnpm", "run", "build", "--script-s=/bin/sh"}, "--script-shell", true},
		{"bun --cw=", "bun", []string{"bun", "run", "build", "--cw=/tmp"}, "--cwd", true},
		{"cargo --mani=", "cargo", []string{"cargo", "test", "--mani=path"}, "--manifest-path", true},
		{"cargo --conf=", "cargo", []string{"cargo", "test", "--conf=k=v"}, "--config", true},

		// go 的双横线写法。
		{"go --exec=", "go", []string{"go", "test", "--exec=echo"}, "-exec", true},
		{"go --exec separate", "go", []string{"go", "test", "--exec", "echo"}, "-exec", true},
		{"go --toolexec=", "go", []string{"go", "test", "--toolexec=/x"}, "-toolexec", true},
		{"go --vettool=", "go", []string{"go", "test", "--vettool=/x"}, "-vettool", true},
		{"go -toolexec=", "go", []string{"go", "test", "-toolexec=/x"}, "-toolexec", true},
		{"go token boundary", "go", []string{"go", "test", "--executor=x"}, "", false},

		// "--" 被上一个选项当成参数值时，危险选项仍在选项位置。
		{"make -o -- --file", "make", []string{"make", "all", "-o", "--", "--file=evil.mk"}, "--file", true},
		{"make -so -- --file", "make", []string{"make", "all", "-so", "--", "--file=evil.mk"}, "--file", true},
		{"make --old-file -- --file", "make", []string{"make", "all", "--old-file", "--", "--file=evil.mk"}, "--file", true},
		{"make -o -- -f", "make", []string{"make", "all", "-o", "--", "-fevil.mk"}, "-f", true},
		{"go -run -- --toolexec", "go", []string{"go", "test", "-run", "--", "--toolexec=/x"}, "-toolexec", true},
		{"go --run -- --exec", "go", []string{"go", "test", "--run", "--", "--exec=/x"}, "-exec", true},

		// 真正的操作数分隔符之后不再匹配。
		{"git operand marker", "git", []string{"git", "log", "--", "--output"}, "", false},
		{"make operand marker", "make", []string{"make", "all", "--", "--file=evil.mk"}, "", false},
		{"go operand marker", "go", []string{"go", "test", "--", "--exec=evil"}, "", false},
		{"npm operand marker", "npm", []string{"npm", "run", "build", "--", "--prefix=/x"}, "", false},

		// token 边界与普通命令。
		{"git --outputs", "git", []string{"git", "log", "--outputs=x"}, "", false},
		{"git format value", "git", []string{"git", "log", "--format=--output"}, "", false},
		{"npm --prefixes", "npm", []string{"npm", "run", "build", "--prefixes=/x"}, "", false},
		{"git status short", "git", []string{"git", "status", "--short"}, "", false},
		{"no arguments", "make", []string{"make"}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hit, ok := dangerousSuffixHit(c.program, c.tokens)
			if hit != c.hit || ok != c.ok {
				t.Fatalf("dangerousSuffixHit(%q, %#v) = %q, %v; want %q, %v",
					c.program, c.tokens, hit, ok, c.hit, c.ok)
			}
		})
	}
}

// --- 真实 CLI 复现测试 ---

func suffixLookPath(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("系统缺少 %s", name)
	}
	return path
}

func suffixEnv(overrides ...string) []string {
	skip := make(map[string]bool, len(overrides))
	for _, override := range overrides {
		if index := strings.IndexByte(override, '='); index >= 0 {
			skip[override[:index]] = true
		}
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		if index := strings.IndexByte(entry, '='); index >= 0 && skip[entry[:index]] {
			continue
		}
		env = append(env, entry)
	}
	return append(env, overrides...)
}

func runSuffixCommand(t *testing.T, dir string, env []string, name string, args ...string) (string, error) {
	t.Helper()
	command := exec.Command(name, args...)
	if dir != "" {
		command.Dir = dir
	}
	if env != nil {
		command.Env = env
	}
	output, err := command.CombinedOutput()
	return string(output), err
}

func writeSuffixFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDangerousSuffixMakeFormsWithRealCLI(t *testing.T) {
	makePath := suffixLookPath(t, "make")
	version, err := runSuffixCommand(t, "", nil, makePath, "--version")
	if err != nil || !strings.Contains(version, "GNU Make") {
		t.Skip("需要 GNU make")
	}
	dir := t.TempDir()
	writeSuffixFile(t, filepath.Join(dir, "Makefile"), "all:\n\t@printf 'default-ok\\n'\ntest:\n\t@printf 'default-test-ok\\n'\n")
	writeSuffixFile(t, filepath.Join(dir, "evil.mk"), "all:\n\t@printf 'evil-ok\\n'\ntest:\n\t@printf 'evil-test-ok\\n'\n")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	writeSuffixFile(t, filepath.Join(dir, "sub", "Makefile"), "all:\n\t@printf 'sub-ok\\n'\n")
	env := suffixEnv("MAKEFLAGS=", "MFLAGS=", "GNUMAKEFLAGS=", "LC_ALL=C")
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"all", "-fevil.mk"}, "evil-ok"},
		{[]string{"all", "-sfevil.mk"}, "evil-ok"},
		{[]string{"all", "-Csub"}, "sub-ok"},
		{[]string{"all", "--fil=evil.mk"}, "evil-ok"},
		{[]string{"all", "-o", "--", "--file=evil.mk"}, "evil-ok"},
		{[]string{"all", "-so", "--", "--file=evil.mk"}, "evil-ok"},
		{[]string{"all", "--old-file", "--", "--file=evil.mk"}, "evil-ok"},
		{[]string{"all", "--", "--file=evil.mk"}, "default-ok"},
	}
	for _, c := range cases {
		output, err := runSuffixCommand(t, dir, env, makePath, c.args...)
		if err != nil || !strings.Contains(output, c.want) {
			t.Fatalf("make %v：%q %v，期望包含 %q", c.args, output, err, c.want)
		}
	}
}

func TestDangerousSuffixGoFormsWithRealCLI(t *testing.T) {
	goPath := suffixLookPath(t, "go")
	dir := t.TempDir()
	writeSuffixFile(t, filepath.Join(dir, "go.mod"), "module example.com/dangerous-suffix-test\n\ngo 1.18\n")
	writeSuffixFile(t, filepath.Join(dir, "x_test.go"), "package suffixtest\n\nimport \"testing\"\n\nfunc TestNoop(t *testing.T) {}\n")
	env := suffixEnv("GOFLAGS=", "GOWORK=off", "GOTOOLCHAIN=local", "GO111MODULE=on", "GOPROXY=off", "LC_ALL=C")
	const missing = "/nonexistent-dangerous-suffix-tool"
	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"--toolexec=", []string{"test", "--toolexec=" + missing, "./..."}, true},
		{"-run -- --toolexec", []string{"test", "-run", "--", "--toolexec=" + missing, "./..."}, true},
		{"operand marker", []string{"test", "--", "--toolexec=" + missing, "./..."}, false},
	}
	for _, c := range cases {
		output, err := runSuffixCommand(t, dir, env, goPath, c.args...)
		if c.wantErr && (err == nil || !strings.Contains(output, missing)) {
			t.Fatalf("go %v 应接受危险选项：%q %v", c.args, output, err)
		}
		if !c.wantErr && (err != nil || strings.Contains(output, missing)) {
			t.Fatalf("go %v 的 -- 之后不应生效：%q %v", c.args, output, err)
		}
	}
}

func TestDangerousSuffixGitOutputWithRealCLI(t *testing.T) {
	gitPath := suffixLookPath(t, "git")
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	env := suffixEnv("GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	commands := [][]string{
		{"init", "-q", "repo"},
		{"-C", "repo", "config", "user.email", "test@example.com"},
		{"-C", "repo", "config", "user.name", "test"},
		{"-C", "repo", "config", "commit.gpgsign", "false"},
	}
	for _, args := range commands {
		if output, err := runSuffixCommand(t, base, env, gitPath, args...); err != nil {
			t.Fatalf("git %v：%q %v", args, output, err)
		}
	}
	writeSuffixFile(t, filepath.Join(repo, "a.txt"), "hello\n")
	for _, args := range [][]string{{"-C", "repo", "add", "a.txt"}, {"-C", "repo", "commit", "-qm", "init"}} {
		if output, err := runSuffixCommand(t, base, env, gitPath, args...); err != nil {
			t.Fatalf("git %v：%q %v", args, output, err)
		}
	}
	attached := filepath.Join(base, "attached.txt")
	separate := filepath.Join(base, "separate.txt")
	for _, args := range [][]string{
		{"-C", "repo", "log", "--output=" + attached, "-1"},
		{"-C", "repo", "log", "--output", separate, "-1"},
	} {
		if output, err := runSuffixCommand(t, base, env, gitPath, args...); err != nil {
			t.Fatalf("git %v：%q %v", args, output, err)
		}
	}
	for _, path := range []string{attached, separate} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("git --output 未写出 %s：%v", path, err)
		}
	}
	operand := filepath.Join(base, "operand.txt")
	if output, err := runSuffixCommand(t, base, env, gitPath, "-C", "repo", "log", "--", "--output="+operand, "-1"); err != nil {
		t.Fatalf("git 操作数分隔符：%q %v", output, err)
	}
	if _, err := os.Stat(operand); !os.IsNotExist(err) {
		t.Fatalf("git -- 之后的 --output 不应生效：%v", err)
	}
}

func TestDangerousSuffixNpmAbbreviationWithRealCLI(t *testing.T) {
	npmPath := suffixLookPath(t, "npm")
	base := t.TempDir()
	pkg := filepath.Join(base, "pkg")
	if err := os.Mkdir(pkg, 0700); err != nil {
		t.Fatal(err)
	}
	writeSuffixFile(t, filepath.Join(pkg, "package.json"),
		`{"name":"dangerous-suffix-test","version":"1.0.0","private":true,"scripts":{"where":"node -p 'process.cwd()'"}}`)
	env := suffixEnv(
		"npm_config_cache="+filepath.Join(base, "cache"),
		"npm_config_update_notifier=false",
		"npm_config_fund=false",
		"npm_config_audit=false",
		"npm_config_progress=false",
		"npm_config_offline=true",
		"npm_config_loglevel=error",
	)
	expected, err := filepath.EvalSymlinks(pkg)
	if err != nil {
		expected = pkg
	}
	if output, err := runSuffixCommand(t, base, env, npmPath, "--prefix="+pkg, "run", "where"); err != nil || !strings.Contains(output, expected) {
		t.Skipf("当前 npm 环境无法运行 --prefix 基线：%q %v", output, err)
	}
	for _, flag := range []string{"--prefi=" + pkg} {
		output, err := runSuffixCommand(t, base, env, npmPath, flag, "run", "where")
		if err != nil || !strings.Contains(output, expected) {
			t.Skipf("当前 npm 不接受 %s 缩写：%q %v", flag, output, err)
		}
	}
}
