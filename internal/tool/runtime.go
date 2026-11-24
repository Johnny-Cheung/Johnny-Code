package tool

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/sandbox"
)

const (
	// DefaultCommandTimeout 是 bash 和固定查询的默认超时。长测试、构建和
	// 依赖安装经常超过 2 分钟，2 分钟会把正常任务误杀。
	DefaultCommandTimeout = 10 * time.Minute
	// MaxCommandTimeout 是单条命令允许显式指定的上限。
	MaxCommandTimeout     = time.Hour
	DefaultMaxOutputBytes = 256 << 10
)

type Runtime struct {
	WorkDir        string
	CommandTimeout time.Duration
	MaxOutputBytes int
	Env            []string
	// Sandbox 非空时，bash 命令通过它包装执行；权限判断仍由各模式决定。
	Sandbox sandbox.Runner
}

// CommandContext 构造一条命令。沙箱只改变执行位置，不改变权限判断。
func (r Runtime) CommandContext(ctx context.Context, program string, args ...string) *exec.Cmd {
	if r.Sandbox != nil {
		return r.Sandbox.CommandContext(ctx, program, args...)
	}
	return exec.CommandContext(ctx, program, args...)
}

func NewRuntime(workDir string) (Runtime, error) {
	if workDir == "" {
		return Runtime{}, errors.New("工作目录不能为空")
	}
	absolute, err := filepath.Abs(workDir)
	if err != nil {
		return Runtime{}, errors.New("无法解析工作目录")
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return Runtime{}, err
	}
	if !info.IsDir() {
		return Runtime{}, errors.New("工作目录不是目录")
	}
	return normalizeRuntime(Runtime{WorkDir: absolute}), nil
}

func normalizeRuntime(runtime Runtime) Runtime {
	if runtime.CommandTimeout <= 0 {
		runtime.CommandTimeout = DefaultCommandTimeout
	}
	if runtime.MaxOutputBytes <= 0 {
		runtime.MaxOutputBytes = DefaultMaxOutputBytes
	}
	return runtime
}
