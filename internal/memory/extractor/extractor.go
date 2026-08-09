package extractor

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"johnnycode/internal/agent"
	"johnnycode/internal/agents"
	"johnnycode/internal/conversation"
	"johnnycode/internal/llm"
	"johnnycode/internal/memory"
	"johnnycode/internal/permissions"
	"johnnycode/internal/tools"
)

type Deps struct {
	MemoryDir     string
	UserMemoryDir string
	ProjectRoot   string
	Client        llm.Client
	ToolRegistry  *tools.Registry
	Protocol      string
	Conversation  *conversation.Manager
	AppendSystem  func(string)
	DebugLogf     func(format string, args ...any)
}

type Extractor struct {
	deps Deps

	mu                       sync.Mutex
	inFlight                 map[*sync.WaitGroup]struct{}
	lastMemoryMessageIdx     int
	inProgress               bool
	turnsSinceLastExtraction int
	pendingContext           *pendingExtractionCtx
}

type pendingExtractionCtx struct{}

func InitExtractMemories(deps Deps) *Extractor {
	return &Extractor{
		deps:     deps,
		inFlight: make(map[*sync.WaitGroup]struct{}),
	}
}

func (e *Extractor) Execute(ctx context.Context) error {
	if e == nil {
		return nil
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	e.mu.Lock()
	e.inFlight[wg] = struct{}{}
	e.mu.Unlock()
	defer func() {
		wg.Done()
		e.mu.Lock()
		delete(e.inFlight, wg)
		e.mu.Unlock()
	}()

	return e.executeImpl(ctx)
}

func (e *Extractor) executeImpl(ctx context.Context) error {

	e.mu.Lock()
	if e.inProgress {
		e.deps.debugf("[extractMemories] extraction in progress — stashing for trailing run")
		e.pendingContext = &pendingExtractionCtx{}
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()

	return e.runExtraction(ctx, false)
}

func (e *Extractor) runExtraction(ctx context.Context, isTrailingRun bool) error {
	messages := e.deps.Conversation.GetMessages()
	newMessageCount := countModelVisibleMessagesSince(messages, e.lastMemoryMessageIdx)

	if hasMemoryWritesSince(messages, e.lastMemoryMessageIdx, e.deps.ProjectRoot) {
		e.deps.debugf("[extractMemories] skipping — conversation already wrote to memory files")
		e.advanceCursor(len(messages))
		return nil
	}

	if !isTrailingRun {
		e.turnsSinceLastExtraction++
		if e.turnsSinceLastExtraction < 1 {
			return nil
		}
	}
	e.turnsSinceLastExtraction = 0

	e.mu.Lock()
	e.inProgress = true
	e.mu.Unlock()
	startTime := time.Now()

	defer func() {
		e.mu.Lock()
		e.inProgress = false
		trailing := e.pendingContext
		e.pendingContext = nil
		e.mu.Unlock()
		if trailing != nil {
			e.deps.debugf("[extractMemories] running trailing extraction for stashed context")
			_ = e.runExtraction(ctx, true)
		}
	}()

	e.deps.debugf("[extractMemories] starting — %d new messages, memoryDir=%s, userMemoryDir=%s",
		newMessageCount, e.deps.MemoryDir, e.deps.UserMemoryDir)

	var combinedScan []memory.MemoryHeader
	if e.deps.UserMemoryDir != "" {
		userScan, _ := memory.ScanMemoryFiles(ctx, e.deps.UserMemoryDir, "user")
		combinedScan = append(combinedScan, userScan...)
	}
	projectScan, _ := memory.ScanMemoryFiles(ctx, e.deps.MemoryDir, "project")
	combinedScan = append(combinedScan, projectScan...)
	manifest := memory.FormatMemoryManifest(combinedScan)
	extractionPrompt := BuildExtractAutoOnlyPrompt(newMessageCount, manifest, false, e.deps.UserMemoryDir, e.deps.MemoryDir)

	forkedConv := buildExtractorConversation(e.deps.Conversation, extractionPrompt)

	subRegistry := agents.FilterToolsForAgent(e.deps.ToolRegistry, nil, nil, true)

	sandboxRoots := []string{e.deps.MemoryDir}
	if e.deps.UserMemoryDir != "" {
		sandboxRoots = append(sandboxRoots, e.deps.UserMemoryDir)
	}
	subSandbox := permissions.NewPathSandbox(sandboxRoots[0], sandboxRoots[1:]...)
	subChecker := permissions.NewChecker(subSandbox, &permissions.RuleEngine{}, permissions.ModeBypass)

	subAgent := agent.New(e.deps.Client, subRegistry, e.deps.Protocol)
	subAgent.MaxIterations = 5
	subAgent.Checker = subChecker
	subAgent.WorkDir = e.deps.ProjectRoot

	ch := subAgent.Run(ctx, forkedConv)
	for range ch {

	}

	e.advanceCursor(len(messages))

	writtenPaths := extractWrittenPaths(forkedConv.GetMessages())
	e.deps.debugf("[extractMemories] finished in %s, %d files written: %v",
		time.Since(startTime), len(writtenPaths), writtenPaths)

	var memoryPaths []string
	for _, p := range writtenPaths {
		if filepath.Base(p) == memory.AutoMemEntrypointName {
			continue
		}
		memoryPaths = append(memoryPaths, p)
	}

	if len(memoryPaths) > 0 && e.deps.AppendSystem != nil {
		var names []string
		for _, p := range memoryPaths {
			names = append(names, filepath.Base(p))
		}
		e.deps.AppendSystem(fmt.Sprintf("Memory saved: %s", strings.Join(names, ", ")))
	}

	return nil
}

func (e *Extractor) Drain(timeoutMs int) error {
	if e == nil {
		return nil
	}
	if timeoutMs < 0 {
		timeoutMs = 60000
	}

	e.mu.Lock()
	wgs := make([]*sync.WaitGroup, 0, len(e.inFlight))
	for wg := range e.inFlight {
		wgs = append(wgs, wg)
	}
	e.mu.Unlock()

	if len(wgs) == 0 {
		return nil
	}

	done := make(chan struct{})
	go func() {
		for _, wg := range wgs {
			wg.Wait()
		}
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(time.Duration(timeoutMs) * time.Millisecond):
		return nil
	}
}

func (e *Extractor) advanceCursor(to int) {
	e.mu.Lock()
	if to > e.lastMemoryMessageIdx {
		e.lastMemoryMessageIdx = to
	}
	e.mu.Unlock()
}

func countModelVisibleMessagesSince(messages []conversation.Message, sinceIdx int) int {
	if sinceIdx < 0 || sinceIdx > len(messages) {
		return countModelVisible(messages)
	}
	n := 0
	for _, m := range messages[sinceIdx:] {
		if isModelVisibleMessage(m) {
			n++
		}
	}
	return n
}

func countModelVisible(messages []conversation.Message) int {
	n := 0
	for _, m := range messages {
		if isModelVisibleMessage(m) {
			n++
		}
	}
	return n
}

func isModelVisibleMessage(m conversation.Message) bool {
	return m.Role == "user" || m.Role == "assistant"
}

func hasMemoryWritesSince(messages []conversation.Message, sinceIdx int, projectRoot string) bool {
	if sinceIdx < 0 {
		sinceIdx = 0
	}
	if sinceIdx >= len(messages) {
		return false
	}
	for _, m := range messages[sinceIdx:] {
		if m.Role != "assistant" {
			continue
		}
		for _, tu := range m.ToolUses {
			fp := getWrittenFilePath(tu)
			if fp == "" {
				continue
			}
			if memory.IsAutoMemPath(fp, projectRoot) {
				return true
			}
		}
	}
	return false
}

func getWrittenFilePath(tu conversation.ToolUseBlock) string {
	if tu.ToolName != "WriteFile" && tu.ToolName != "EditFile" {
		return ""
	}
	fp, ok := tu.Arguments["file_path"].(string)
	if !ok {
		return ""
	}
	return fp
}

func extractWrittenPaths(messages []conversation.Message) []string {
	var paths []string
	seen := make(map[string]struct{})
	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		for _, tu := range m.ToolUses {
			fp := getWrittenFilePath(tu)
			if fp == "" {
				continue
			}
			if _, ok := seen[fp]; ok {
				continue
			}
			seen[fp] = struct{}{}
			paths = append(paths, fp)
		}
	}
	return paths
}

func buildExtractorConversation(parent *conversation.Manager, prompt string) *conversation.Manager {
	forked := conversation.NewManager()
	for _, msg := range parent.GetMessages() {
		switch msg.Role {
		case "assistant":
			if len(msg.ToolUses) > 0 {
				forked.AddAssistantMessageWithTools(msg.Content, msg.ToolUses)
			} else {
				forked.AddAssistantMessage(msg.Content)
			}
		default:
			if len(msg.ToolResults) > 0 {
				forked.AddToolResultsMessage(msg.ToolResults)
			} else {
				forked.AddUserMessage(msg.Content)
			}
		}
	}
	forked.AddUserMessage(prompt)
	return forked
}

func (d Deps) debugf(format string, args ...any) {
	if d.DebugLogf != nil {
		d.DebugLogf(format, args...)
	}
}
