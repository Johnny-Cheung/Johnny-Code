package session

import (
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
)

func TestRecordNoticeAndSummary(t *testing.T) {
	recorder := newTestRecorder(t)
	if err := recorder.Record(agent.Event{Type: agent.EventNotice, UserTurn: 1, Notice: "正在压缩"}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record(agent.Event{Type: agent.EventSummary, UserTurn: 1, Summary: &agent.SummaryInfo{
		Text: "摘要正文", Before: 1000, After: 200, CoveredThrough: 2,
	}}); err != nil {
		t.Fatal(err)
	}
	lines := readJSONLines(t, recorder.Path())
	joined := string(strings.Join(bytesToStrings(lines), "\n"))
	if !strings.Contains(joined, `"notice":"正在压缩"`) {
		t.Fatalf("notice 未记录：%s", joined)
	}
	if !strings.Contains(joined, `"summary"`) || !strings.Contains(joined, `"text":"摘要正文"`) || !strings.Contains(joined, `"after_tokens":200`) {
		t.Fatalf("summary 未记录：%s", joined)
	}
}

func bytesToStrings(lines [][]byte) []string {
	result := make([]string, len(lines))
	for i, line := range lines {
		result[i] = string(line)
	}
	return result
}
