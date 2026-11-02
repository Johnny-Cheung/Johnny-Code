package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

// 恢复相关的错误。调用方据此区分“会话坏了”“正在使用”“没有可恢复的轮次”。
var (
	ErrNoCompleteTurn = errors.New("会话没有完整轮次")
	ErrSessionInUse   = errors.New("会话正在被其他进程使用")
	ErrNotASession    = errors.New("不是有效的会话记录")
)

const titleLimit = 80

// Info 是会话列表中的一行。它只包含定位和展示信息，不加载消息正文。
type Info struct {
	ID             string
	Path           string
	Time           time.Time
	Modified       time.Time
	WorkDir        string
	API            string
	Model          string
	PermissionMode string
	Turns          int
	Title          string
	LastStatus     agent.Status
	// Incomplete 表示最后一条 end 之后还有内容，或者根本没有 end。
	Incomplete bool
	// Locked 表示另一个进程正持有该会话的排他锁。
	Locked bool
}

// Step 是恢复时按顺序重放的一条消息。工具结果额外带上原始调用，
// 供请求视图按同样的规则重新生成预览。
type Step struct {
	UserTurn int
	Message  model.Message
	Call     *model.ToolCall
}

// ResumeSummary 是最近一次完整历史压缩的结果。
type ResumeSummary struct {
	Text           string
	CoveredThrough int
	Note           bool
	BeforeTokens   int
	AfterTokens    int
}

// ResumeState 是从会话日志恢复出来的状态。Steps 已经按摘要裁剪，
// 等于原进程最后一次请求视图里的消息；更早的细节仍留在 Path 指向的文件中。
type ResumeState struct {
	Metadata   Metadata
	Path       string
	Steps      []Step
	Summary    *ResumeSummary
	UserTurn   int
	LastStatus agent.Status
	// Dropped 表示最后一条 end 之后有不完整内容，恢复时已丢弃。
	Dropped bool

	Recorder *Recorder
	lock     *Lock
}

// Close 关闭追加写句柄和文件锁。
func (s *ResumeState) Close() error {
	if s == nil {
		return nil
	}
	var errs []error
	if s.Recorder != nil {
		errs = append(errs, s.Recorder.Close())
		s.Recorder = nil
	}
	if s.lock != nil {
		errs = append(errs, s.lock.Close())
		s.lock = nil
	}
	return errors.Join(errs...)
}

// SessionsDir 返回 ~/.johnny/sessions。
func SessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".johnny", "sessions"), nil
}

// LogPath 返回一个会话 ID 对应的日志路径。
func LogPath(id string) (string, error) {
	id = strings.TrimSpace(id)
	if !validSessionID(id) {
		return "", fmt.Errorf("会话 ID 无效")
	}
	dir, err := SessionsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, id+".jsonl"), nil
}

// List 列出当前用户的所有主会话，按修改时间倒序。子 Agent 的记录被排除。
func List() ([]Info, error) {
	dir, err := SessionsDir()
	if err != nil {
		return nil, err
	}
	return ListDir(dir)
}

// ListDir 列出指定目录中的主会话。损坏的文件被跳过。
func ListDir(dir string) ([]Info, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取会话目录失败: %w", err)
	}
	infos := make([]Info, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		fileInfo, err := entry.Info()
		if err != nil {
			continue
		}
		header, err := scanHeader(path)
		if err != nil || header.meta.ID == "" || header.meta.ParentID != "" || header.lastEndOffset == 0 {
			continue
		}
		infos = append(infos, Info{
			ID:             header.meta.ID,
			Path:           path,
			Time:           header.meta.Time,
			Modified:       fileInfo.ModTime(),
			WorkDir:        header.meta.CWD,
			API:            header.meta.API,
			Model:          header.meta.Model,
			PermissionMode: header.meta.PermissionMode,
			Turns:          header.turns,
			Title:          header.title,
			LastStatus:     header.lastStatus,
			Incomplete:     header.incomplete,
			Locked:         sessionLocked(path),
		})
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].Modified.Equal(infos[j].Modified) {
			return infos[i].ID > infos[j].ID
		}
		return infos[i].Modified.After(infos[j].Modified)
	})
	return infos, nil
}

// LoadResume 按会话 ID 加载恢复状态。
func LoadResume(id string) (*ResumeState, error) {
	path, err := LogPath(id)
	if err != nil {
		return nil, err
	}
	return LoadResumePath(path)
}

// LoadResumePath 加载一个会话文件并持有排他锁，返回可直接追加写的 Recorder。
// 只恢复到最后一个有 end 的完整轮次；之后的不完整内容会被丢弃。
func LoadResumePath(path string) (*ResumeState, error) {
	lock, err := lockExclusive(path)
	if err != nil {
		return nil, err
	}
	release := func() { _ = lock.Close() }

	header, err := scanHeader(path)
	if err != nil {
		release()
		return nil, err
	}
	if header.meta.ID == "" {
		release()
		return nil, ErrNotASession
	}
	if header.lastEndOffset <= 0 {
		release()
		return nil, ErrNoCompleteTurn
	}
	// 最后一个完整轮次之后的内容（执行到一半退出、损坏的尾行）直接丢弃，
	// 避免恢复后追加的记录与半截记录混在一起。丢弃的内容不再参与恢复。
	if header.incomplete {
		if err := os.Truncate(path, header.lastEndOffset); err != nil {
			release()
			return nil, fmt.Errorf("截断未完成轮次失败: %w", err)
		}
	}
	events, err := readEvents(path, header.lastEndOffset)
	if err != nil {
		release()
		return nil, err
	}
	steps, summary, err := resumeSteps(events)
	if err != nil {
		release()
		return nil, err
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, sessionFileMode)
	if err != nil {
		release()
		return nil, fmt.Errorf("打开会话日志失败: %w", err)
	}
	return &ResumeState{
		Metadata: Metadata{
			ID:             header.meta.ID,
			WorkDir:        header.meta.CWD,
			API:            header.meta.API,
			Model:          header.meta.Model,
			PermissionMode: header.meta.PermissionMode,
		},
		Path:       path,
		Steps:      steps,
		Summary:    summary,
		UserTurn:   header.lastEndUserTurn,
		LastStatus: header.lastStatus,
		Dropped:    header.incomplete,
		Recorder:   newRecorder(file, path),
		lock:       lock,
	}, nil
}

type headerInfo struct {
	meta             metaEvent
	title            string
	turns            int
	lastEndOffset    int64
	lastEndUserTurn  int
	lastStatus       agent.Status
	lastContentStart int64
	incomplete       bool
}

// scanHeader 只解析列表和恢复所需的最小字段，避免为了列会话把每条工具结果
// 都解码一遍。记录由 json.Marshal 生成，type 是第一个字段；其他情况回退。
func scanHeader(path string) (headerInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return headerInfo{}, err
	}
	defer file.Close()

	var header headerInfo
	reader := bufio.NewReaderSize(file, 64*1024)
	var offset int64
	for {
		line, readErr := reader.ReadBytes('\n')
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			if typeName := fastRecordType(trimmed); typeName != "" {
				switch typeName {
				case metaRecordType:
					var record metaEvent
					if json.Unmarshal(trimmed, &record) == nil {
						header.meta = record
					}
				case "start":
					if header.title == "" {
						var record struct {
							Message *messageRecord `json:"message"`
						}
						if json.Unmarshal(trimmed, &record) == nil {
							header.title = messageTitle(record.Message)
						}
					}
				case "end":
					var record struct {
						UserTurn int          `json:"user_turn"`
						Status   agent.Status `json:"status"`
					}
					if json.Unmarshal(trimmed, &record) == nil {
						header.turns++
						header.lastEndOffset = offset + int64(len(line))
						header.lastEndUserTurn = record.UserTurn
						header.lastStatus = record.Status
					}
				}
			}
			header.lastContentStart = offset + int64(len(line))
		}
		offset += int64(len(line))
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return headerInfo{}, fmt.Errorf("读取会话日志失败: %w", readErr)
		}
	}
	header.incomplete = header.lastEndOffset == 0 || header.lastContentStart > header.lastEndOffset
	return header, nil
}

// fastRecordType 读取记录开头的 type。JSONL 由本包 json.Marshal 生成，
// type 是 eventRecord 的第一个字段；遇到其他格式时回退到完整解码。
func fastRecordType(line []byte) string {
	const prefix = `{"type":"`
	if bytes.HasPrefix(line, []byte(prefix)) {
		rest := line[len(prefix):]
		if end := bytes.IndexByte(rest, '"'); end > 0 {
			return string(rest[:end])
		}
	}
	var probe struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(line, &probe) == nil {
		return probe.Type
	}
	return ""
}

func readEvents(path string, limit int64) ([]eventRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var events []eventRecord
	reader := bufio.NewReaderSize(file, 64*1024)
	var offset int64
	for offset < limit {
		line, readErr := reader.ReadBytes('\n')
		if len(line) == 0 {
			break
		}
		if offset+int64(len(line)) > limit {
			break
		}
		offset += int64(len(line))
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		var record eventRecord
		if err := json.Unmarshal(trimmed, &record); err != nil {
			return nil, fmt.Errorf("会话记录格式无效（偏移 %d）: %w", offset, err)
		}
		events = append(events, record)
		if readErr != nil {
			break
		}
	}
	return events, nil
}

func resumeSteps(events []eventRecord) ([]Step, *ResumeSummary, error) {
	steps := make([]Step, 0, len(events))
	var summary *ResumeSummary
	for _, event := range events {
		switch event.Type {
		case "start", "inject":
			if event.Message == nil {
				continue
			}
			message, err := modelMessage(event.Message)
			if err != nil {
				return nil, nil, err
			}
			steps = append(steps, Step{UserTurn: event.UserTurn, Message: message})
		case "response":
			if event.Response == nil || event.Response.Message == nil {
				continue
			}
			message, err := modelMessage(event.Response.Message)
			if err != nil {
				return nil, nil, err
			}
			steps = append(steps, Step{UserTurn: event.UserTurn, Message: message})
		case "tool_end", "tool_skipped":
			if event.Call == nil || event.Result == nil {
				continue
			}
			call, err := modelToolCall(event.Call)
			if err != nil {
				return nil, nil, err
			}
			result := *event.Result
			if result.ToolCallID == "" {
				result.ToolCallID = call.ID
			}
			steps = append(steps, Step{
				UserTurn: event.UserTurn,
				Message: model.Message{Role: model.RoleTool, Content: []model.ContentBlock{{
					Type: model.BlockToolResult, ToolResult: &result,
				}}},
				Call: &call,
			})
		case "summary":
			if event.Summary == nil {
				continue
			}
			summary = &ResumeSummary{
				Text:           event.Summary.Text,
				CoveredThrough: event.Summary.CoveredThrough,
				Note:           event.Summary.Note,
				BeforeTokens:   event.Summary.BeforeTokens,
				AfterTokens:    event.Summary.AfterTokens,
			}
		}
	}
	if summary != nil && summary.CoveredThrough > 0 {
		retained := steps[:0]
		for _, step := range steps {
			if step.UserTurn > summary.CoveredThrough {
				retained = append(retained, step)
			}
		}
		steps = retained
	}
	return steps, summary, nil
}

func modelMessage(record *messageRecord) (model.Message, error) {
	if record == nil {
		return model.Message{}, ErrNotASession
	}
	message := model.Message{Role: record.Role, Source: record.Source}
	for _, block := range record.Content {
		converted := model.ContentBlock{Type: block.Type, Text: block.Text}
		if block.ToolCall != nil {
			call, err := modelToolCall(block.ToolCall)
			if err != nil {
				return model.Message{}, err
			}
			converted.ToolCall = &call
		}
		if block.ToolResult != nil {
			result := *block.ToolResult
			converted.ToolResult = &result
		}
		if block.Replay != nil {
			replay, ok := modelReplay(block.Replay)
			if ok {
				converted.Replay = replay
			}
		}
		message.Content = append(message.Content, converted)
	}
	return message, nil
}

func modelToolCall(record *toolCallRecord) (model.ToolCall, error) {
	if record == nil || strings.TrimSpace(record.ID) == "" {
		return model.ToolCall{}, ErrNotASession
	}
	call := model.ToolCall{ID: record.ID, Name: record.Name}
	if raw := strings.TrimSpace(record.ArgumentsRaw); raw != "" {
		if !json.Valid([]byte(raw)) {
			return model.ToolCall{}, fmt.Errorf("工具调用参数不是有效 JSON: %w", ErrNotASession)
		}
		call.Arguments = json.RawMessage(raw)
	}
	return call, nil
}

func modelReplay(record *replayRecord) (*model.Replay, bool) {
	if record == nil {
		return nil, false
	}
	if len(record.Data) > 0 && json.Valid(record.Data) {
		return &model.Replay{Source: record.Source, Data: record.Data}, true
	}
	if raw := strings.TrimSpace(record.DataRaw); raw != "" && json.Valid([]byte(raw)) {
		return &model.Replay{Source: record.Source, Data: json.RawMessage(raw)}, true
	}
	return nil, false
}

func messageTitle(record *messageRecord) string {
	if record == nil {
		return ""
	}
	for _, block := range record.Content {
		if block.Type != model.BlockText {
			continue
		}
		text := strings.Join(strings.Fields(block.Text), " ")
		if text == "" {
			continue
		}
		if utf8.RuneCountInString(text) > titleLimit {
			runes := []rune(text)
			text = string(runes[:titleLimit-1]) + "…"
		}
		return text
	}
	return ""
}
