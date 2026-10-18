package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Johnny-Cheung/Johnny-Code/internal/config"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		// 恢复默认信号处理，让再次 Ctrl+C 可以强制退出。
		stop()
	}()
	code := run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string) int {
	prompt := strings.TrimSpace(strings.Join(args, " "))
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "用法：go run ./cmd/johnny \"你想说的话\"")
		return 1
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "配置错误：", err)
		return 1
	}

	fmt.Fprintln(os.Stderr, "正在等待模型回复…")
	if err := model.Stream(ctx, cfg, prompt, os.Stdout); err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			fmt.Fprintln(os.Stderr, "请求已取消。")
			return 130
		case errors.Is(err, context.DeadlineExceeded):
			fmt.Fprintln(os.Stderr, "请求超时（最多等待 5 分钟），请检查服务状态或重试。")
		default:
			fmt.Fprintln(os.Stderr, "调用失败：", err)
		}
		return 1
	}

	return 0
}
