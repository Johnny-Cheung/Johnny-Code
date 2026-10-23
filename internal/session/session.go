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
	WorkDir        string
	API            string
	Model          string
	PermissionMode string
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

	id, err := newSessionID()
	if err != nil {
		return nil, fmt.Errorf("generate session id: %w", err)
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
	case agent.EventEnd:
		record.Status = event.Status
	}

	return r.write(record)
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
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

type metaEvent struct {
	Type           string    `json:"type"`
	ID             string    `json:"id"`
	Time           time.Time `json:"time"`
	CWD            string    `json:"cwd"`
	API            string    `json:"api"`
	Model          string    `json:"model"`
	PermissionMode string    `json:"permission_mode,omitempty"`
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
	Status   agent.Status      `json:"status,omitempty"`
	Error    string            `json:"error,omitempty"`
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
