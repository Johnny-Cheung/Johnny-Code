// Package session writes a diagnostic JSONL log for one agent task.
//
// The log is append-only for the lifetime of a Recorder. It is never read by
// this package and is not a source of conversation memory.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

const (
	metaRecordType  = "meta"
	sessionDirMode  = 0o700
	sessionFileMode = 0o600
)

var errRecorderClosed = errors.New("session recorder is closed")

// Metadata identifies the task that produced a session log. It deliberately
// contains no credentials or environment snapshot.
type Metadata struct {
	ID             string
	WorkDir        string
	API            string
	Model          string
	PermissionMode string
	// 以下字段仅子 Agent 的会话记录使用，把它与父会话和子任务关联起来。
	ParentID    string
	SubagentID  string
	Kind        string
	Access      string
	Paths       []string
	Description string
}

// SubagentRecord 是父会话中一次子任务的起止记录。它只保存定位和结果摘要，
// 不保存子任务的过程；过程在子任务自己的会话记录里。
type SubagentRecord struct {
	Phase        string // start 或 end
	ID           string
	Description  string
	Kind         string
	Access       string
	Paths        []string
	ChildSession string
	Status       string
	Requests     int
	DurationMS   int64
	Partial      bool
	Truncated    bool
	Injected     bool
	Error        string
}

// PermissionRecord is one approval decision. It is deliberately separate from
// model tool results: the decision records what the user chose, while the tool
// result records what the tool eventually returned.
type PermissionRecord struct {
	ToolCallID string           `json:"tool_call_id"`
	Tool       string           `json:"tool"`
	Target     string           `json:"target"`
	Decision   string           `json:"decision"`
	Source     string           `json:"source,omitempty"`
	Reason     string           `json:"reason,omitempty"`
	Prefixes   [][]string       `json:"recorded_prefixes,omitempty"`
	Paths      []PermissionPath `json:"recorded_paths,omitempty"`
	// Origin 是发起请求的子任务 ID；主 Agent 的请求为空。
	Origin string `json:"origin,omitempty"`
}

type PermissionPath struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// MemoryRecord 记录一次长期记忆或用户规则写入。它只保存定位信息，
// 不保存记忆正文或工具输出。
type MemoryRecord struct {
	Scope  string `json:"scope"`
	Action string `json:"action"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Source string `json:"source,omitempty"`
	Rule   bool   `json:"rule,omitempty"`
}

func clonePermissionPrefixes(prefixes [][]string) [][]string {
	if len(prefixes) == 0 {
		return nil
	}
	cloned := make([][]string, len(prefixes))
	for i, prefix := range prefixes {
		cloned[i] = append([]string(nil), prefix...)
	}
	return cloned
}

// Recorder writes JSONL records synchronously. Record and Close return I/O
// errors to the caller; the recorder never logs or panics on its own.
type Recorder struct {
	mu     sync.Mutex
	writer io.WriteCloser
	path   string
	now    func() time.Time
	closed bool
}

// New creates a new session file under ~/.johnny/sessions and writes its
// metadata record. The file is created exclusively with mode 0600.
func New(meta Metadata) (*Recorder, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}

	dir := filepath.Join(home, ".johnny", "sessions")
	if err := os.MkdirAll(dir, sessionDirMode); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}
	if err := os.Chmod(dir, sessionDirMode); err != nil {
		return nil, fmt.Errorf("set session directory permissions: %w", err)
	}

	id := strings.TrimSpace(meta.ID)
	if id == "" {
		id, err = NewID()
		if err != nil {
			return nil, fmt.Errorf("generate session id: %w", err)
		}
	}
	if !validSessionID(id) {
		return nil, fmt.Errorf("session id is invalid")
	}
	path := filepath.Join(dir, id+".jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, sessionFileMode)
	if err != nil {
		return nil, fmt.Errorf("create session log: %w", err)
	}

	recorder := newRecorder(file, path)
	metaRecord := metaEvent{
		Type:           metaRecordType,
		ID:             id,
		Time:           recorder.timestamp(),
		CWD:            meta.WorkDir,
		API:            meta.API,
		Model:          meta.Model,
		PermissionMode: meta.PermissionMode,
		ParentID:       meta.ParentID,
		SubagentID:     meta.SubagentID,
		Kind:           meta.Kind,
		Access:         meta.Access,
		Paths:          meta.Paths,
		Description:    meta.Description,
	}
	if err := recorder.write(metaRecord); err != nil {
		_ = recorder.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write session metadata: %w", err)
	}
	return recorder, nil
}

// newRecorder exists so tests can observe write and close failures. Callers
// outside this package should use New so the file and metadata are created.
func newRecorder(writer io.WriteCloser, path string) *Recorder {
	return &Recorder{
		writer: writer,
		path:   path,
		now:    time.Now,
	}
}

// Path returns the JSONL file path for this session.
func (r *Recorder) Path() string {
	return r.path
}

// Record writes one event as a JSON line. Streaming model deltas are skipped;
// the complete Response event is recorded instead. A malformed raw tool
// argument or replay payload is logged losslessly as a string rather than
// making the whole record fail. UserTurn identifies the user input in a
// multi-turn conversation; Turn still counts model requests inside that input.
// A skipped tool call is recorded like a tool_end so the diagnostic log keeps
// the complete call/result history.
func (r *Recorder) Record(event agent.Event) error {
	if event.Type == agent.EventModel {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errRecorderClosed
	}

	record := eventRecord{
		Type:     string(event.Type),
		Time:     r.timestamp(),
		UserTurn: event.UserTurn,
		Turn:     event.Turn,
		Sources:  event.Sources,
		Error:    event.Error,
	}

	if event.Type == agent.EventResponse && event.Response != nil {
		record.Response = newResponse(event.Response)
	} else if event.Message != nil {
		record.Message = newMessage(event.Message)
	}

	switch event.Type {
	case agent.EventToolStart:
		record.Call = newToolCall(event.Call)
	case agent.EventToolEnd, agent.EventToolSkipped:
		record.Call = newToolCall(event.Call)
		record.Result = event.ToolResult
		record.Artifact = event.Artifact
	case agent.EventNotice:
		record.Notice = event.Notice
	case agent.EventSummary:
		record.Summary = newSummary(event.Summary)
	case agent.EventEnd:
		record.Status = event.Status
	}

	return r.write(record)
}

// RecordPermission writes one approval decision record. It is independent of
// agent.Event and may be called before the first tool result is recorded.
func (r *Recorder) RecordPermission(record PermissionRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errRecorderClosed
	}
	return r.write(permissionEvent{
		Type:       "permission",
		Time:       r.timestamp(),
		ToolCallID: record.ToolCallID,
		Tool:       record.Tool,
		Target:     record.Target,
		Decision:   record.Decision,
		Source:     record.Source,
		Reason:     record.Reason,
		Prefixes:   clonePermissionPrefixes(record.Prefixes),
		Paths:      record.Paths,
		Origin:     record.Origin,
	})
}

// RecordMemory writes one memory or user-rule write record.
func (r *Recorder) RecordMemory(record MemoryRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errRecorderClosed
	}
	return r.write(memoryEvent{
		Type:   "memory_write",
		Time:   r.timestamp(),
		Scope:  record.Scope,
		Action: record.Action,
		Name:   record.Name,
		Path:   record.Path,
		Source: record.Source,
		Rule:   record.Rule,
	})
}

// RecordSubagent writes the start or end of one background subagent.
func (r *Recorder) RecordSubagent(record SubagentRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errRecorderClosed
	}
	return r.write(subagentEvent{
		Type:         "subagent_" + record.Phase,
		Time:         r.timestamp(),
		ID:           record.ID,
		Description:  record.Description,
		Kind:         record.Kind,
		Access:       record.Access,
		Paths:        record.Paths,
		ChildSession: record.ChildSession,
		Status:       record.Status,
		Requests:     record.Requests,
		DurationMS:   record.DurationMS,
		Partial:      record.Partial,
		Truncated:    record.Truncated,
		Injected:     record.Injected,
		Error:        record.Error,
	})
}

// Close closes the underlying file. It is safe to call more than once.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if err := r.writer.Close(); err != nil {
		return fmt.Errorf("close session log: %w", err)
	}
	return nil
}

func (r *Recorder) timestamp() time.Time {
	return r.now().UTC()
}

func (r *Recorder) write(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode session record: %w", err)
	}
	data = append(data, '\n')

	n, err := r.writer.Write(data)
	if err != nil {
		return fmt.Errorf("write session record: %w", err)
	}
	if n != len(data) {
		return fmt.Errorf("write session record: %w", io.ErrShortWrite)
	}
	return nil
}

func newSessionID() (string, error) {
	return NewID()
}

// NewID 生成会话 ID。会话日志和工具输出存档共用同一个 ID。
func NewID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validSessionID(id string) bool {
	return sessionIDPattern.MatchString(id)
}

type metaEvent struct {
	Type           string    `json:"type"`
	ID             string    `json:"id"`
	Time           time.Time `json:"time"`
	CWD            string    `json:"cwd"`
	API            string    `json:"api"`
	Model          string    `json:"model"`
	PermissionMode string    `json:"permission_mode,omitempty"`
	ParentID       string    `json:"parent_id,omitempty"`
	SubagentID     string    `json:"subagent_id,omitempty"`
	Kind           string    `json:"kind,omitempty"`
	Access         string    `json:"access,omitempty"`
	Paths          []string  `json:"paths,omitempty"`
	Description    string    `json:"description,omitempty"`
}

type subagentEvent struct {
	Type         string    `json:"type"`
	Time         time.Time `json:"time"`
	ID           string    `json:"subagent_id"`
	Description  string    `json:"description"`
	Kind         string    `json:"kind"`
	Access       string    `json:"access"`
	Paths        []string  `json:"paths,omitempty"`
	ChildSession string    `json:"child_session,omitempty"`
	Status       string    `json:"status,omitempty"`
	Requests     int       `json:"requests,omitempty"`
	DurationMS   int64     `json:"duration_ms,omitempty"`
	Partial      bool      `json:"partial,omitempty"`
	Truncated    bool      `json:"truncated,omitempty"`
	Injected     bool      `json:"injected,omitempty"`
	Error        string    `json:"error,omitempty"`
}

type eventRecord struct {
	Type     string            `json:"type"`
	Time     time.Time         `json:"time"`
	UserTurn int               `json:"user_turn"`
	Turn     int               `json:"turn"`
	Message  *messageRecord    `json:"message,omitempty"`
	Response *responseRecord   `json:"response,omitempty"`
	Call     *toolCallRecord   `json:"call,omitempty"`
	Result   *model.ToolResult `json:"result,omitempty"`
	Artifact string            `json:"artifact,omitempty"`
	Notice   string            `json:"notice,omitempty"`
	Summary  *summaryRecord    `json:"summary,omitempty"`
	Sources  []string          `json:"sources,omitempty"`
	Status   agent.Status      `json:"status,omitempty"`
	Error    string            `json:"error,omitempty"`
}

type summaryRecord struct {
	Text           string `json:"text"`
	BeforeTokens   int    `json:"before_tokens"`
	AfterTokens    int    `json:"after_tokens"`
	CoveredThrough int    `json:"covered_through,omitempty"`
	Note           bool   `json:"note,omitempty"`
}

func newSummary(summary *agent.SummaryInfo) *summaryRecord {
	if summary == nil {
		return nil
	}
	return &summaryRecord{
		Text:           summary.Text,
		BeforeTokens:   summary.Before,
		AfterTokens:    summary.After,
		CoveredThrough: summary.CoveredThrough,
		Note:           summary.Note,
	}
}

type permissionEvent struct {
	Type       string           `json:"type"`
	Time       time.Time        `json:"time"`
	ToolCallID string           `json:"tool_call_id"`
	Tool       string           `json:"tool"`
	Target     string           `json:"target"`
	Decision   string           `json:"decision"`
	Source     string           `json:"source,omitempty"`
	Reason     string           `json:"reason,omitempty"`
	Prefixes   [][]string       `json:"recorded_prefixes,omitempty"`
	Paths      []PermissionPath `json:"recorded_paths,omitempty"`
	Origin     string           `json:"origin,omitempty"`
}

type memoryEvent struct {
	Type   string    `json:"type"`
	Time   time.Time `json:"time"`
	Scope  string    `json:"scope"`
	Action string    `json:"action"`
	Name   string    `json:"name"`
	Path   string    `json:"path"`
	Source string    `json:"source,omitempty"`
	Rule   bool      `json:"rule,omitempty"`
}

type responseRecord struct {
	Message    *messageRecord   `json:"message"`
	StopReason model.StopReason `json:"stop_reason"`
	Usage      usageRecord      `json:"usage"`
}

type usageRecord struct {
	InputTokens      *int64 `json:"input_tokens,omitempty"`
	OutputTokens     *int64 `json:"output_tokens,omitempty"`
	CacheReadTokens  *int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens *int64 `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  *int64 `json:"reasoning_tokens,omitempty"`
}

type messageRecord struct {
	Role    model.Role           `json:"role"`
	Content []contentBlockRecord `json:"content"`
	Source  *model.Source        `json:"source,omitempty"`
}

type contentBlockRecord struct {
	Type       model.BlockType   `json:"type"`
	Text       string            `json:"text,omitempty"`
	ToolCall   *toolCallRecord   `json:"tool_call,omitempty"`
	ToolResult *model.ToolResult `json:"tool_result,omitempty"`
	Replay     *replayRecord     `json:"replay,omitempty"`
}

type toolCallRecord struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ArgumentsRaw string `json:"arguments_raw"`
}

type replayRecord struct {
	Source  model.Source    `json:"source"`
	Data    json.RawMessage `json:"data,omitempty"`
	DataRaw string          `json:"data_raw,omitempty"`
}

func newResponse(response *model.Response) *responseRecord {
	return &responseRecord{
		Message:    newMessage(&response.Message),
		StopReason: response.StopReason,
		Usage: usageRecord{
			InputTokens:      response.Usage.InputTokens,
			OutputTokens:     response.Usage.OutputTokens,
			CacheReadTokens:  response.Usage.CacheReadTokens,
			CacheWriteTokens: response.Usage.CacheWriteTokens,
			ReasoningTokens:  response.Usage.ReasoningTokens,
		},
	}
}

func newMessage(message *model.Message) *messageRecord {
	if message == nil {
		return nil
	}
	record := &messageRecord{
		Role:   message.Role,
		Source: message.Source,
	}
	if message.Content != nil {
		record.Content = make([]contentBlockRecord, len(message.Content))
		for i, block := range message.Content {
			record.Content[i] = contentBlockRecord{
				Type:       block.Type,
				Text:       block.Text,
				ToolCall:   newToolCall(block.ToolCall),
				ToolResult: block.ToolResult,
				Replay:     newReplay(block.Replay),
			}
		}
	}
	return record
}

func newToolCall(call *model.ToolCall) *toolCallRecord {
	if call == nil {
		return nil
	}
	return &toolCallRecord{
		ID:           call.ID,
		Name:         call.Name,
		ArgumentsRaw: string(call.Arguments),
	}
}

func newReplay(replay *model.Replay) *replayRecord {
	if replay == nil {
		return nil
	}
	record := &replayRecord{Source: replay.Source}
	if json.Valid(replay.Data) {
		record.Data = append(json.RawMessage(nil), replay.Data...)
	} else {
		record.DataRaw = string(replay.Data)
	}
	return record
}
