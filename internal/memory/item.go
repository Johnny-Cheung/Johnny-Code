package memory

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
	"gopkg.in/yaml.v3"
)

type Scope string

const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
)

func ParseScope(value string) (Scope, bool) {
	switch Scope(strings.TrimSpace(value)) {
	case ScopeUser:
		return ScopeUser, true
	case ScopeProject:
		return ScopeProject, true
	default:
		return "", false
	}
}

type Type string

const (
	TypePreference  Type = "preference"
	TypeFeedback    Type = "feedback"
	TypeProject     Type = "project"
	TypeReference   Type = "reference"
	TypeEnvironment Type = "environment"
)

func (t Type) Valid() bool {
	switch t {
	case TypePreference, TypeFeedback, TypeProject, TypeReference, TypeEnvironment:
		return true
	default:
		return false
	}
}

type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

func (c Confidence) Valid() bool {
	switch c {
	case ConfidenceHigh, ConfidenceMedium, ConfidenceLow:
		return true
	default:
		return false
	}
}

// Item 是一条长期记忆。Scope 和 Project 由存储目录决定，不信任文件内声明。
type Item struct {
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	Type        Type       `yaml:"type"`
	Scope       Scope      `yaml:"scope"`
	Project     string     `yaml:"project,omitempty"`
	Created     time.Time  `yaml:"created"`
	Updated     time.Time  `yaml:"updated"`
	Source      string     `yaml:"source,omitempty"`
	Confidence  Confidence `yaml:"confidence"`
	Body        string     `yaml:"-"`
	Path        string     `yaml:"-"`
}

var itemNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

const (
	MaxDescriptionTokens = 40
	MaxBodyBytes         = 8 << 10
)

var errSecret = errors.New("记忆包含疑似密钥或凭据")

// Validate 校验写入前的记忆条目。Created/Updated 由 Store 负责补齐。
func (i *Item) Validate() error {
	if !itemNamePattern.MatchString(i.Name) {
		return fmt.Errorf("记忆名称无效：%q；只允许小写字母、数字和连字符，最长 64 字符", i.Name)
	}
	if strings.TrimSpace(i.Description) == "" {
		return errors.New("记忆描述不能为空")
	}
	if strings.ContainsAny(i.Description, "\r\n") {
		return errors.New("记忆描述必须是一行")
	}
	if contextview.EstimateText(i.Description) > MaxDescriptionTokens {
		return fmt.Errorf("记忆描述超过 %d token 上限", MaxDescriptionTokens)
	}
	if !i.Type.Valid() {
		return fmt.Errorf("记忆类型无效：%q", i.Type)
	}
	if _, ok := ParseScope(string(i.Scope)); !ok {
		return fmt.Errorf("记忆作用域无效：%q", i.Scope)
	}
	if i.Scope == ScopeProject && strings.TrimSpace(i.Project) == "" {
		return errors.New("项目记忆缺少 project")
	}
	if i.Confidence == "" {
		i.Confidence = ConfidenceMedium
	}
	if !i.Confidence.Valid() {
		return fmt.Errorf("记忆置信度无效：%q", i.Confidence)
	}
	if strings.TrimSpace(i.Body) == "" {
		return errors.New("记忆正文不能为空")
	}
	if len([]byte(i.Body)) > MaxBodyBytes {
		return fmt.Errorf("记忆正文超过 %d 字节上限", MaxBodyBytes)
	}
	if containsSecret(i.Name) || containsSecret(i.Description) || containsSecret(i.Body) {
		return errSecret
	}
	return nil
}

// Markdown 生成带 frontmatter 的记忆文件。
func (i Item) Markdown() ([]byte, error) {
	if i.Created.IsZero() {
		i.Created = time.Now()
	}
	if i.Updated.IsZero() {
		i.Updated = i.Created
	}
	if i.Confidence == "" {
		i.Confidence = ConfidenceMedium
	}
	front, err := yaml.Marshal(i)
	if err != nil {
		return nil, err
	}
	body := strings.TrimSpace(i.Body)
	var builder strings.Builder
	builder.WriteString("---\n")
	builder.Write(front)
	builder.WriteString("---\n\n")
	builder.WriteString(body)
	builder.WriteByte('\n')
	return []byte(builder.String()), nil
}

// ParseItem 解析记忆文件。文件名由 Store 负责校验，Scope/Project 由调用方补齐。
func ParseItem(path string, data []byte) (Item, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	front, body, err := splitFrontmatter(text)
	if err != nil {
		return Item{}, err
	}
	var item Item
	if err := yaml.Unmarshal([]byte(front), &item); err != nil {
		return Item{}, fmt.Errorf("记忆 frontmatter 无效：%w", err)
	}
	item.Body = strings.Trim(body, "\n")
	item.Path = path
	if item.Confidence == "" {
		item.Confidence = ConfidenceMedium
	}
	if err := item.Validate(); err != nil {
		return Item{}, err
	}
	return item, nil
}

func splitFrontmatter(text string) (string, string, error) {
	if !strings.HasPrefix(text, "---\n") {
		return "", "", errors.New("记忆文件必须以 --- frontmatter 开头")
	}
	rest := text[len("---\n"):]
	index := strings.Index(rest, "\n---")
	if index < 0 {
		return "", "", errors.New("记忆文件缺少 frontmatter 结束标记")
	}
	front := rest[:index]
	body := rest[index+len("\n---"):]
	body = strings.TrimPrefix(body, "\n")
	body = strings.TrimPrefix(body, "\n")
	return front, body, nil
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}\b`),
	regexp.MustCompile(`\bghp_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)\b(JOHNNY_API_KEY|ANTHROPIC_AUTH_TOKEN|OPENAI_API_KEY|PASSWORD|SECRET)\s*=\s*\S+`),
}

func containsSecret(text string) bool {
	for _, pattern := range secretPatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

// OneLine 将描述压缩为一行，供索引和工具输出使用。
func OneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// RuneLimit 按 UTF-8 边界截断文本。
func RuneLimit(text string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit])
}
