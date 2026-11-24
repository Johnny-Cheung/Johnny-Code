//go:build darwin

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Options 是创建 Seatbelt 沙箱所需的配置。
type Options struct {
	WorkDir string
	Network bool
}

// Seatbelt 使用 macOS 自带的 sandbox-exec 执行 bash 命令。策略文件是运行时
// 生成的，用户不需要维护 SBPL。
type Seatbelt struct {
	workDir     string
	dir         string
	profilePath string
	network     bool
}

// Available 检查 sandbox-exec 是否存在并可用。macOS 自带该命令，但它是
// deprecated 接口，启动时做一次自检，避免把“没有沙箱”误当成“有沙箱”。
func Available() error {
	path, err := exec.LookPath("sandbox-exec")
	if err != nil {
		return errors.New("未找到 sandbox-exec")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path, "-p", "(version 1)(allow default)", "/usr/bin/true")
	if err := command.Run(); err != nil {
		return fmt.Errorf("sandbox-exec 自检失败：%w", err)
	}
	return nil
}

// NewSeatbelt 创建会话级沙箱目录和策略文件。
func NewSeatbelt(opts Options) (Runner, error) {
	if strings.TrimSpace(opts.WorkDir) == "" {
		return nil, errors.New("沙箱工作目录不能为空")
	}
	workDir := realPath(opts.WorkDir)
	dir, err := os.MkdirTemp("", "johnny-sandbox-")
	if err != nil {
		return nil, fmt.Errorf("创建沙箱目录失败：%w", err)
	}
	dir = realPath(dir)
	for _, name := range []string{"tmp", "cache", "npm-cache", "pip-cache"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("创建沙箱缓存目录失败：%w", err)
		}
	}
	profilePath := filepath.Join(dir, "profile.sb")
	if err := os.WriteFile(profilePath, []byte(buildProfile(workDir, dir, opts.Network)), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("写入沙箱策略失败：%w", err)
	}
	return &Seatbelt{workDir: workDir, dir: dir, profilePath: profilePath, network: opts.Network}, nil
}

func (s *Seatbelt) CommandContext(ctx context.Context, program string, args ...string) *exec.Cmd {
	full := make([]string, 0, 4+len(args))
	full = append(full, "-f", s.profilePath, "--", program)
	full = append(full, args...)
	return exec.CommandContext(ctx, "sandbox-exec", full...)
}

func (s *Seatbelt) Environment(base []string) []string {
	if base == nil {
		base = os.Environ()
	}
	values := make(map[string]string, len(base)+5)
	order := make([]string, 0, len(base)+5)
	for _, entry := range base {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, exists := values[name]; !exists {
			order = append(order, name)
		}
		values[name] = value
	}
	overrides := map[string]string{
		"TMPDIR":           filepath.Join(s.dir, "tmp"),
		"GOTMPDIR":         filepath.Join(s.dir, "tmp"),
		"XDG_CACHE_HOME":   filepath.Join(s.dir, "cache"),
		"npm_config_cache": filepath.Join(s.dir, "npm-cache"),
		"PIP_CACHE_DIR":    filepath.Join(s.dir, "pip-cache"),
	}
	for name, value := range overrides {
		if _, exists := values[name]; !exists {
			order = append(order, name)
		}
		values[name] = value
	}
	env := make([]string, 0, len(order))
	for _, name := range order {
		env = append(env, name+"="+values[name])
	}
	return env
}

func (s *Seatbelt) Info() Info {
	return Info{Name: "seatbelt", Network: s.network}
}

func (s *Seatbelt) Describe() string {
	if s.network {
		return "Seatbelt（网络开启）"
	}
	return "Seatbelt（网络关闭）"
}

func (s *Seatbelt) Close() error {
	if s == nil || s.dir == "" {
		return nil
	}
	return os.RemoveAll(s.dir)
}

func buildProfile(workDir, sessionDir string, network bool) string {
	writePaths := []string{workDir, sessionDir}
	if temp := realPath("/tmp"); temp != "" {
		writePaths = append(writePaths, temp)
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		for _, path := range []string{
			filepath.Join(home, "Library", "Caches"),
			filepath.Join(home, "go", "pkg", "mod"),
			filepath.Join(home, ".cache"),
		} {
			if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
				writePaths = append(writePaths, realPath(path))
			}
		}
	}

	var builder strings.Builder
	builder.WriteString("(version 1)\n")
	builder.WriteString("(allow default)\n\n")
	builder.WriteString("(deny file-write*)\n")
	builder.WriteString("(allow file-write*")
	for _, path := range writePaths {
		builder.WriteString(" (subpath ")
		builder.WriteString(sbplQuote(path))
		builder.WriteString(")")
	}
	for _, path := range []string{"/dev/null", "/dev/tty", "/dev/stdout", "/dev/stderr"} {
		builder.WriteString(" (literal ")
		builder.WriteString(sbplQuote(path))
		builder.WriteString(")")
	}
	builder.WriteString(" (subpath \"/dev/fd\")")
	builder.WriteString(")\n")

	// bash 不应该修改 Johnny 自己的项目级配置。Git hooks 不单独拒绝：
	// 工作目录本身可写，拒绝它反而会让 `git init` 无法创建 .git/hooks。
	builder.WriteString("(deny file-write* (subpath ")
	builder.WriteString(sbplQuote(filepath.Join(workDir, ".johnny")))
	builder.WriteString("))\n")

	// 沙箱命令不能借 Docker/Colima/OrbStack 的 socket 逃逸到宿主机，
	// 也不能读取 Johnny 自己的会话记录。
	if err == nil && home != "" {
		builder.WriteString("(deny file-read* (subpath ")
		builder.WriteString(sbplQuote(filepath.Join(home, ".johnny")))
		builder.WriteString("))\n")
		for _, socket := range []string{
			filepath.Join(home, ".docker", "run", "docker.sock"),
			filepath.Join(home, ".colima", "default", "docker.sock"),
			filepath.Join(home, ".orbstack", "run", "docker.sock"),
		} {
			builder.WriteString("(deny file-read* (literal ")
			builder.WriteString(sbplQuote(socket))
			builder.WriteString("))\n")
		}
	}
	for _, socket := range []string{"/var/run/docker.sock", "/private/var/run/docker.sock"} {
		builder.WriteString("(deny file-read* (literal ")
		builder.WriteString(sbplQuote(socket))
		builder.WriteString("))\n")
	}

	if !network {
		builder.WriteString("(deny network*)\n")
	}
	return builder.String()
}

func sbplQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

func realPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs
	}
	return resolved
}
