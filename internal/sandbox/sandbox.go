package sandbox

import (
	"context"
	"os/exec"
)

// Info 是写入审计的最小沙箱信息。
type Info struct {
	Name    string
	Network bool
}

// Runner 把命令包装到某个执行环境中。权限策略只决定命令是否允许执行，
// 具体在宿主机还是沙箱里执行由 Runner 决定。
type Runner interface {
	CommandContext(ctx context.Context, program string, args ...string) *exec.Cmd
	// Environment 返回在沙箱内执行命令时应使用的环境变量。
	Environment(base []string) []string
	// Info 返回写入权限审计的沙箱标识和网络状态。
	Info() Info
	Describe() string
	Close() error
}
