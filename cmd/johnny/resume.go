package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
	"github.com/Johnny-Cheung/Johnny-Code/internal/session"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tui"
)

func handleTerminalResult(code int, err error, input io.Reader, output, stderr io.Writer) int {
	var request *tui.ResumeRequest
	if errors.As(err, &request) {
		return relaunchWithResume(request.ID, input, output, stderr)
	}
	if err != nil {
		return reportError(stderr, "终端界面失败：", err)
	}
	return code
}

// relaunchWithResume 用同一个可执行文件重新启动到目标会话。当前 TUI 已经
// 恢复终端状态，新进程重新进入 TUI；旧进程等待新进程结束后退出，因此不会
// 同时持有两份会话锁。
func relaunchWithResume(id string, input io.Reader, output, stderr io.Writer) int {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "恢复会话失败：无法定位 johnny 可执行文件：", err)
		return 1
	}
	childArgs := stripResumeArgs(os.Args[1:])
	env := os.Environ()
	// PTY 测试用的 helper 进程不能直接接收 --resume：它由 go test 启动。
	// 用环境变量把目标会话传给它，由 helper 自己拼进 run 的参数。
	if os.Getenv("JOHNNY_TUI_TEST_HELPER") == "1" {
		env = setEnv(env, "JOHNNY_TUI_TEST_RESUME", id)
	} else {
		childArgs = append(childArgs, "--resume", id)
	}
	command := exec.Command(executable, childArgs...)
	command.Stdin, command.Stdout, command.Stderr = input, output, stderr
	command.Env = env
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(stderr, "恢复会话失败：", err)
		return 1
	}
	return 0
}

func setEnv(env []string, name, value string) []string {
	prefix := name + "="
	cleaned := env[:0]
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			cleaned = append(cleaned, entry)
		}
	}
	return append(cleaned, prefix+value)
}

func stripResumeArgs(args []string) []string {
	cleaned := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--resume":
			index++
		case strings.HasPrefix(arg, "--resume="):
		default:
			cleaned = append(cleaned, arg)
		}
	}
	return cleaned
}

func resumeError(err error) error {
	switch {
	case errors.Is(err, session.ErrNoCompleteTurn):
		return errors.New("该会话没有完整轮次，无法恢复")
	case errors.Is(err, session.ErrSessionInUse):
		return errors.New("该会话正在被另一个 johnny 进程使用")
	case errors.Is(err, os.ErrNotExist):
		return errors.New("找不到该会话记录")
	default:
		return err
	}
}

func resumeContextNote(state *session.ResumeState) string {
	if state == nil {
		return ""
	}
	if state.Summary != nil && state.Summary.CoveredThrough > 0 {
		note := fmt.Sprintf("## 恢复的会话\n本会话从本地记录恢复，摘要覆盖到第 %d 轮。更早的细节可用 history 工具查询。", state.Summary.CoveredThrough)
		if state.Dropped {
			note += " 上次未完成的一轮已丢弃。"
		}
		return note
	}
	note := "## 恢复的会话\n本会话从本地记录恢复，最近的消息已经载入。更早的工具输出预览可用 history 工具查询。"
	if state.Dropped {
		note += " 上次未完成的一轮已丢弃。"
	}
	return note
}

func resumeNoticeText(state *session.ResumeState, mode permissionMode) string {
	if state == nil {
		return ""
	}
	text := fmt.Sprintf("已恢复会话 %s：%d 轮，权限 %s", state.Metadata.ID, state.UserTurn, mode.label())
	if state.Summary != nil && state.Summary.CoveredThrough > 0 {
		text += fmt.Sprintf("，摘要覆盖到第 %d 轮", state.Summary.CoveredThrough)
	}
	text += "。"
	if state.Dropped {
		text += " 上次有一轮未完成，已丢弃。"
	}
	return text
}

func restoreSummary(summary *session.ResumeSummary) *contextview.RestoreSummary {
	if summary == nil {
		return nil
	}
	return &contextview.RestoreSummary{
		Text:           summary.Text,
		CoveredThrough: summary.CoveredThrough,
		Note:           summary.Note,
	}
}

func restoreSteps(steps []session.Step) []contextview.RestoreStep {
	if len(steps) == 0 {
		return nil
	}
	restored := make([]contextview.RestoreStep, len(steps))
	for i, step := range steps {
		restored[i] = contextview.RestoreStep{
			UserTurn: step.UserTurn,
			Message:  step.Message,
			Call:     step.Call,
		}
	}
	return restored
}

// stripReplay 在模型或协议变化时丢弃续接数据。Replay 只对原服务、原协议和
// 原模型有效，跨协议恢复时保留它可能让第一次请求直接失败。
func stripReplay(steps []session.Step) {
	for i := range steps {
		for j := range steps[i].Message.Content {
			if steps[i].Message.Content[j].Replay != nil {
				steps[i].Message.Content[j].Replay = nil
			}
		}
	}
}

func sameWorkDir(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	return canonicalDir(left) == canonicalDir(right)
}

// resumableSessions 返回当前工作目录下的主会话。/resume 只允许切换同一目录的
// 会话，避免恢复后工具、路径规则和 AGENTS 使用错误的工作目录。
func resumableSessions(workDir, currentID string) ([]tui.SessionSummary, error) {
	infos, err := session.List()
	if err != nil {
		return nil, err
	}
	items := make([]tui.SessionSummary, 0, len(infos))
	for _, info := range infos {
		if !sameWorkDir(info.WorkDir, workDir) {
			continue
		}
		items = append(items, tui.SessionSummary{
			ID:         info.ID,
			Title:      info.Title,
			WorkDir:    info.WorkDir,
			Model:      info.Model,
			Turns:      info.Turns,
			Modified:   info.Modified,
			Incomplete: info.Incomplete,
			Locked:     info.Locked,
			Current:    info.ID == currentID,
		})
	}
	return items, nil
}

func canonicalDir(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved
	}
	return filepath.Clean(absolute)
}
