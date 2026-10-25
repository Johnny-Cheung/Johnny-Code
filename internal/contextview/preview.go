package contextview

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Preview 是一个工具结果进入请求视图时的表示。
type Preview struct {
	Content       string
	Truncated     bool
	Artifact      string
	OriginalBytes int
	NextOffset    int
	Warning       string
}

// BuildPreview 对超过 limit 的工具结果保留有界预览。bash 使用头尾预览并尽量
// 存档；read 保留开头并给出继续读取的 offset；其他工具保留开头。
func BuildPreview(tool, content string, args json.RawMessage, store *ArtifactStore, turn, call, limit int) Preview {
	if limit <= 0 {
		limit = DefaultPreviewBytes
	}
	preview := Preview{Content: content, OriginalBytes: len(content)}
	if len(content) <= limit {
		return preview
	}
	preview.Truncated = true
	if tool == "bash" {
		headLimit, tailLimit := limit*3/4, limit/4
		head := cutHeadAtLine(content, headLimit)
		tail := cutTailAtLine(content, len(content)-tailLimit)
		var artifact string
		if store.Enabled() {
			path, err := store.Save(turn, call, tool, content)
			if err != nil {
				preview.Warning = "工具输出存档失败：" + err.Error()
			} else {
				artifact = path
				preview.Artifact = path
			}
		}
		var marker string
		switch {
		case artifact != "":
			marker = fmt.Sprintf("\n\n[上下文预览：原始输出 %d 字节，超过 %d KiB；已保留开头和结尾。完整输出：%s]\n\n", len(content), limit>>10, artifact)
		case preview.Warning != "":
			marker = fmt.Sprintf("\n\n[上下文预览：原始输出 %d 字节，超过 %d KiB；已保留开头和结尾。%s；完整输出未存档]\n\n", len(content), limit>>10, preview.Warning)
		default:
			marker = fmt.Sprintf("\n\n[上下文预览：原始输出 %d 字节，超过 %d KiB；已保留开头和结尾，未启用存档]\n\n", len(content), limit>>10)
		}
		preview.Content = head + marker + tail
		return preview
	}

	head := cutHeadAtLine(content, limit)
	switch tool {
	case "read":
		offset := argumentInt(args, "offset", 1)
		lines := strings.Count(head, "\n")
		if lines == 0 {
			preview.NextOffset = offset
			preview.Content = head + fmt.Sprintf("\n[上下文预览：内容超过 %d KiB，且首行本身超过预览上限；offset 无法继续读取该行，可使用 bash 分段查看。]\n", limit>>10)
			break
		}
		preview.NextOffset = offset + lines
		preview.Content = head + fmt.Sprintf("\n[上下文预览：文件内容超过 %d KiB，已保留到第 %d 行；可使用 read 的 offset=%d 继续读取。]\n", limit>>10, offset+lines-1, preview.NextOffset)
	case "skill":
		preview.Content = head + fmt.Sprintf("\n[上下文预览：Skill 正文超过 %d KiB，已保留开头；完整正文仍保存在本地 Skill 文件中。]\n", limit>>10)
	default:
		preview.Content = head + fmt.Sprintf("\n[上下文预览：工具结果超过 %d KiB，已保留开头。]\n", limit>>10)
	}
	return preview
}

// cutHeadAtLine 在字节上限内尽量保留完整行，并回退到 UTF-8 边界。
func cutHeadAtLine(content string, limit int) string {
	if limit >= len(content) {
		return content
	}
	if limit <= 0 {
		return ""
	}
	head := content[:limit]
	if index := strings.LastIndexByte(head, '\n'); index >= 0 {
		head = head[:index+1]
	}
	return trimInvalidTail(head)
}

// cutTailAtLine 从尾部保留不超过 limit 字节，并尽量从完整行开始。
func cutTailAtLine(content string, start int) string {
	if start <= 0 {
		return content
	}
	if start >= len(content) {
		return ""
	}
	tail := content[start:]
	if index := strings.IndexByte(tail, '\n'); index >= 0 && index+1 < len(tail) {
		tail = tail[index+1:]
	}
	return trimInvalidHead(tail)
}

func trimInvalidTail(text string) string {
	for len(text) > 0 {
		r, size := utf8.DecodeLastRuneInString(text)
		if r != utf8.RuneError || size > 1 {
			break
		}
		text = text[:len(text)-1]
	}
	return text
}

func trimInvalidHead(text string) string {
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		if r != utf8.RuneError || size > 1 {
			break
		}
		text = text[1:]
	}
	return text
}

func argumentInt(raw json.RawMessage, key string, fallback int) int {
	var value struct {
		Offset *int `json:"offset"`
	}
	if key != "offset" || json.Unmarshal(raw, &value) != nil || value.Offset == nil || *value.Offset < 1 {
		return fallback
	}
	return *value.Offset
}

func argumentString(raw json.RawMessage, key string) string {
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	var text string
	if json.Unmarshal(value[key], &text) == nil {
		return strings.TrimSpace(text)
	}
	return ""
}
