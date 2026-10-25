// Package contextview 管理每次模型请求实际携带的上下文视图。
//
// 完整历史仍由调用方持有；本包只维护经过预览、存档和压缩后的请求视图。
package contextview

const (
	DefaultWindow       = 1_000_000
	DefaultOutputTokens = 4096
	MinSafetyTokens     = 16_384
	DefaultPreviewBytes = 32 << 10
	BashHeadBytes       = 24 << 10
	BashTailBytes       = 8 << 10
	CompactPreviewBytes = 1024
	EmergencyBytes      = 512
	TurnPreviewBudget   = 128 << 10
	LimitedPreviewBytes = 8 << 10
	DefaultRetainTurns  = 5

	TurnTriggerPercent    = 90
	RequestTriggerPercent = 95

	CompactMinSavedTokens = 4096
)

// Budget 是模型窗口、输出预留和安全余量组成的输入预算。
type Budget struct {
	Window int
	Output int
	Safety int
	Usable int
}

// NewBudget 按 W - O - S 计算可用输入预算。
func NewBudget(window, output int) Budget {
	if window <= 0 {
		window = DefaultWindow
	}
	if output <= 0 {
		output = DefaultOutputTokens
	}
	safety := max(MinSafetyTokens, window/20)
	usable := window - output - safety
	if usable <= 0 {
		// 异常配置仍保留一个可工作的下限；调用方负责校验窗口是否合理。
		usable = max(1024, window/2)
	}
	return Budget{Window: window, Output: output, Safety: safety, Usable: usable}
}

func (b Budget) TurnTrigger() int {
	return b.Usable * TurnTriggerPercent / 100
}

func (b Budget) RequestTrigger() int {
	return b.Usable * RequestTriggerPercent / 100
}

// Valid 检查配置是否足以进行请求级治理。
func (b Budget) Valid() bool {
	return b.Window > b.Output && b.Usable > 0
}
