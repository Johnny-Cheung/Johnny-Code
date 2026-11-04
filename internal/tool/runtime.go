package tool

import (
	"errors"
	"os"
	"path/filepath"
	"time"
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
