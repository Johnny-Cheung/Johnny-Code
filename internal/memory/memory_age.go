
package memory

import (
	"fmt"
	"time"
)




func MemoryAgeDays(mtimeMs int64) int {
	d := (time.Now().UnixMilli() - mtimeMs) / 86_400_000
	if d < 0 {
		return 0
	}
	return int(d)
}




func MemoryAge(mtimeMs int64) string {
	d := MemoryAgeDays(mtimeMs)
	if d == 0 {
		return "today"
	}
	if d == 1 {
		return "yesterday"
	}
	return fmt.Sprintf("%d days ago", d)
}











func MemoryFreshnessText(mtimeMs int64) string {
	d := MemoryAgeDays(mtimeMs)
	if d <= 1 {
		return ""
	}
	return fmt.Sprintf(
		"This memory is %d days old. "+
			"Memories are point-in-time observations, not live state — "+
			"claims about code behavior or file:line citations may be outdated. "+
			"Verify against current code before asserting as fact.",
		d,
	)
}




func MemoryFreshnessNote(mtimeMs int64) string {
	text := MemoryFreshnessText(mtimeMs)
	if text == "" {
		return ""
	}
	return fmt.Sprintf("<system-reminder>%s</system-reminder>\n", text)
}
