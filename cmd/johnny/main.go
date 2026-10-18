package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Johnny-Cheung/Johnny-Code/internal/config"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

var (
	version          = "0.2.0"
	commit           = "dev"
	configFileBase64 string
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		// 恢复默认信号处理，让再次 Ctrl+C 可以强制退出。
		stop()
	}()
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, input io.Reader, output, stderr io.Writer) int {
	flags := flag.NewFlagSet("johnny", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var help, showVersion bool
	flags.BoolVar(&help, "help", false, "显示帮助")
	flags.BoolVar(&help, "h", false, "显示帮助")
	flags.BoolVar(&showVersion, "version", false, "显示版本")
	flags.Usage = func() { printHelp(stderr) }
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if help {
		printHelp(output)
		return 0
	}
	if showVersion {
		fmt.Fprintf(output, "johnny v%s (%s)\n", version, commit)
		return 0
	}

	path, err := configPath()
	if err != nil {
		return reportError(stderr, "配置错误：", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return reportError(stderr, "配置错误：", err)
	}

	prompt := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if len(flags.Args()) == 0 {
		fmt.Fprint(stderr, "请输入问题：")
		prompt, err = readPrompt(ctx, input)
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(stderr)
			return 0
		}
		if err != nil {
			return reportError(stderr, "读取输入失败：", err)
		}
	}
	if prompt == "" {
		fmt.Fprintln(stderr, "问题不能为空。")
		return 1
	}

	fmt.Fprintln(stderr, "正在等待模型回复…")
	if err := model.Stream(ctx, cfg, prompt, output); err != nil {
		return reportError(stderr, "调用失败：", err)
	}
	return 0
}

func configPath() (string, error) {
	path := ".env"
	if configFileBase64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(configFileBase64)
		if err != nil || len(decoded) == 0 {
			return "", errors.New("构建中的配置文件路径无效，请重新安装 johnny")
		}
		path = string(decoded)
	}
	return filepath.Abs(path)
}

func printHelp(output io.Writer) {
	path, err := configPath()
	if err != nil {
		path = "构建路径无效，请重新安装 johnny"
	}
	fmt.Fprintf(output, `Johnny-Code

用法：
  johnny "问题"     流式回复一次后退出
  johnny            输入一个问题，流式回复后退出

选项：
  -h, --help        显示帮助
  --version         显示版本和提交号

配置文件：%s
Ctrl+C 取消请求，等待输入时 Ctrl+D 退出。
`, path)
}

func readPrompt(ctx context.Context, input io.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	type result struct {
		text string
		err  error
	}
	ready := make(chan result, 1)
	go func() {
		text, err := bufio.NewReader(input).ReadString('\n')
		ready <- result{text, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case res := <-ready:
		text := strings.TrimSpace(res.text)
		if errors.Is(res.err, io.EOF) && text != "" {
			return text, nil
		}
		return text, res.err
	}
}

func reportError(output io.Writer, prefix string, err error) int {
	switch {
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(output, "请求已取消。")
		return 130
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintln(output, "请求超时（最多等待 5 分钟），请检查服务状态或重试。")
	default:
		fmt.Fprintln(output, prefix, err)
	}
	return 1
}
