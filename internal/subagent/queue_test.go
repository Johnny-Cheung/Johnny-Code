package subagent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func readRequests(count int) []tool.SpawnRequest {
	requests := make([]tool.SpawnRequest, 0, count)
	for i := 1; i <= count; i++ {
		requests = append(requests, freshReadRequest(fmt.Sprintf("T%d", i)))
	}
	return requests
}

// blockUntilReleased 让每个子任务的模型请求停在原地，直到测试放行一个名额或任务被取消。
func blockUntilReleased(h *harness, tokens chan struct{}, started *[]string, mu *sync.Mutex) {
	h.respond = func(ctx context.Context, req model.Request) (model.Response, error) {
		mu.Lock()
		*started = append(*started, strings.Join(requestTexts(req), " "))
		mu.Unlock()
		select {
		case <-tokens:
			return textResponse("完成"), nil
		case <-ctx.Done():
			return model.Response{}, ctx.Err()
		}
	}
}

func TestSpawnAllQueuesBeyondCapacityAndRefillsInOrder(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	var mu sync.Mutex
	var started []string
	tokens := make(chan struct{})
	blockUntilReleased(h, tokens, &started, &mu)

	ids, err := h.enqueueBatch(readRequests(MaxConcurrent + 3))
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != MaxConcurrent+3 || h.manager.Running() != MaxConcurrent+3 {
		t.Fatalf("整批都应登记：%d %d", len(ids), h.manager.Running())
	}
	h.waitFor("名额内的任务启动", func() bool { return len(h.eventsOf(EventStarted)) == MaxConcurrent })
	queued := 0
	for _, info := range h.manager.Snapshot() {
		if info.State == StateQueued {
			queued++
		}
	}
	if queued != 3 {
		t.Fatalf("多出的 3 个任务应排队：%d", queued)
	}
	for i := 0; i < 3; i++ {
		tokens <- struct{}{}
		want := MaxConcurrent + i + 1
		h.waitFor("排队的任务补位", func() bool { return len(h.eventsOf(EventStarted)) == want })
	}
	events := h.eventsOf(EventStarted)
	for i, want := range []string{"T6", "T7", "T8"} {
		if got := events[MaxConcurrent+i].Info.Description; got != want {
			t.Fatalf("排队的任务应按先进先出启动，第 %d 个补位的应是 %s：%s", i+1, want, got)
		}
	}
	for i := 0; i < MaxConcurrent; i++ {
		tokens <- struct{}{}
	}
	h.waitEnded(MaxConcurrent + 3)
	h.manager.Wait()
	if h.manager.Running() != 0 || h.parent.InboxLen() != MaxConcurrent+3 {
		t.Fatalf("全部结束后运行表应为空、报告应各自进入收件箱：%d %d", h.manager.Running(), h.parent.InboxLen())
	}
}

func TestSpawnAllRollsBackWholeBatchOnFailure(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.respond = func(context.Context, model.Request) (model.Response, error) { return textResponse("不应启动"), nil }
	overlapping := []tool.SpawnRequest{
		freshReadRequest("读取"),
		{Description: "写 A", Prompt: "写", Kind: tool.SpawnFresh, Access: tool.SpawnWrite, Paths: []string{h.path("pkg")}},
		{Description: "写 B", Prompt: "写", Kind: tool.SpawnFresh, Access: tool.SpawnWrite, Paths: []string{h.path("pkg", "sub")}},
	}
	if _, err := h.enqueueBatch(overlapping); err == nil {
		t.Fatal("范围重叠应整批拒绝")
	}
	missingPaths := []tool.SpawnRequest{
		{Description: "写 A", Prompt: "写", Kind: tool.SpawnFresh, Access: tool.SpawnWrite, Paths: []string{h.path("pkg")}},
		{Description: "缺范围", Prompt: "写", Kind: tool.SpawnFresh, Access: tool.SpawnWrite},
	}
	if _, err := h.enqueueBatch(missingPaths); err == nil {
		t.Fatal("参数无效应整批拒绝")
	}
	if h.manager.Running() != 0 || len(h.manager.claims.Owners()) != 0 || len(h.eventsOf(EventStarted)) != 0 {
		t.Fatal("被拒绝的整批不应启动任何任务，也不应留下文件范围")
	}
	h.manager.Wait()
}

func TestCancelAllDropsQueuedTasksWithoutEvents(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	var mu sync.Mutex
	var started []string
	blockUntilReleased(h, make(chan struct{}), &started, &mu)
	requests := append(readRequests(MaxConcurrent), tool.SpawnRequest{
		Description: "排队的写入", Prompt: "写", Kind: tool.SpawnFresh, Access: tool.SpawnWrite, Paths: []string{h.path("pkg")},
	})
	if _, err := h.enqueueBatch(requests); err != nil {
		t.Fatal(err)
	}
	h.waitFor("名额内的任务启动", func() bool { return len(h.eventsOf(EventStarted)) == MaxConcurrent })
	if len(h.manager.claims.Owners()) != 1 {
		t.Fatal("排队的写入类任务应已登记文件范围")
	}
	if count := h.manager.CancelAll(); count != MaxConcurrent+1 {
		t.Fatalf("应取消运行中和排队的全部任务：%d", count)
	}
	h.manager.Wait()
	for _, ended := range h.waitEnded(MaxConcurrent) {
		if ended.Status != agent.StatusCanceled || ended.Injected {
			t.Fatalf("被取消的任务不应注入：%+v", ended)
		}
	}
	if len(h.eventsOf(EventStarted)) != MaxConcurrent || len(h.eventsOf(EventEnded)) != MaxConcurrent {
		t.Fatal("从未启动的排队任务不应产生启动或结束事件")
	}
	if h.manager.Running() != 0 || len(h.manager.claims.Owners()) != 0 || h.parent.InboxLen() != 0 {
		t.Fatal("取消后应清空运行表、排队、文件范围和收件箱")
	}
}

func TestSpawnIsRejectedWhileTasksAreQueued(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	var mu sync.Mutex
	var started []string
	blockUntilReleased(h, make(chan struct{}), &started, &mu)
	if _, err := h.enqueueBatch(readRequests(MaxConcurrent + 1)); err != nil {
		t.Fatal(err)
	}
	_, err := h.manager.Spawn(context.Background(), freshReadRequest("插队"))
	if err == nil || !strings.Contains(err.Error(), "1 个在排队") {
		t.Fatalf("有任务排队时单个委派应被拒绝并说明排队数：%v", err)
	}
	h.manager.CancelAll()
	h.manager.Wait()
}
