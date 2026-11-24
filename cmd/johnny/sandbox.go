package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/sandbox"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

var errSandboxExit = errors.New("用户选择退出沙箱初始化")

// prepareSandbox 在启动时检测 macOS Seatbelt，并把执行器挂到 Runtime。
// 检测失败时默认退出；用户可以在交互终端里显式选择本次不使用沙箱继续。
func prepareSandbox(runtime *tool.Runtime, interactive bool, input io.Reader, output io.Writer, disabled, network bool) (bool, error) {
	if runtime == nil {
		return false, errors.New("工具运行时不能为空")
	}
	disabled = disabled || disabledByEnv()
	if disabled {
		fmt.Fprintln(output, "警告：沙箱已关闭（JOHNNY_SANDBOX=off）；bash 将直接在宿主机执行。")
		return false, nil
	}
	network = network || sandboxNetworkEnabled()
	for {
		err := sandbox.Available()
		if err == nil {
			runner, createErr := sandbox.NewSeatbelt(sandbox.Options{
				WorkDir: runtime.WorkDir,
				Network: network,
			})
			if createErr == nil {
				runtime.Sandbox = runner
				runtime.Env = runner.Environment(runtime.Env)
				fmt.Fprintf(output, "沙箱：%s\n", runner.Describe())
				return true, nil
			}
			err = createErr
		}
		if !interactive || input == nil {
			return false, fmt.Errorf("沙箱不可用：%w；请确认 macOS 的 sandbox-exec 可用，或设置 JOHNNY_SANDBOX=off 后启动", err)
		}

		fmt.Fprintf(output, "沙箱不可用：%s\n", escapeTerminal(err.Error()))
		fmt.Fprintln(output, "沙箱用于把 bash 限制在工作目录、临时目录和缓存目录内，")
		fmt.Fprintln(output, "避免误删项目外文件；网络默认关闭。")
		fmt.Fprintln(output, "没有沙箱时，bash 会直接在宿主机执行，模型生成的命令可能修改或删除项目外的文件。")
		fmt.Fprintln(output)
		fmt.Fprintln(output, "[Enter] 退出并处理沙箱（推荐）")
		fmt.Fprintln(output, "[r]     重试检测")
		fmt.Fprintln(output, "[c]     本次不使用沙箱继续（不安全）")
		answer, readErr := readConfirmationLine(input)
		if readErr != nil {
			return false, fmt.Errorf("读取沙箱选择失败：%w", readErr)
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "r":
			continue
		case "c":
			fmt.Fprintln(output, "警告：本次会话不使用沙箱；bash 将直接在宿主机执行。")
			return false, nil
		default:
			return false, errSandboxExit
		}
	}
}

func disabledByEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("JOHNNY_SANDBOX"))) {
	case "off", "0", "false", "no":
		return true
	default:
		return false
	}
}

func sandboxNetworkEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("JOHNNY_SANDBOX_NETWORK"))) {
	case "on", "1", "true", "yes":
		return true
	default:
		return false
	}
}

func sandboxPromptNote(runner sandbox.Runner) string {
	if runner == nil {
		return ""
	}
	return fmt.Sprintf("\n\n执行环境：bash 运行在 %s 沙箱中，只能写工作目录、临时目录和缓存目录。", runner.Describe())
}
