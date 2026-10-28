package tool

import "context"

// Origin 标识发起工具调用的子 Agent。零值表示主 Agent。
type Origin struct {
	ID          string
	Description string
}

func (o Origin) IsZero() bool { return o.ID == "" }

type originKey struct{}

// WithOrigin 返回携带子 Agent 身份的 context。审批请求和审计记录据此标明来源。
func WithOrigin(ctx context.Context, origin Origin) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, originKey{}, origin)
}

// OriginFrom 返回 context 中的子 Agent 身份；没有时返回零值。
func OriginFrom(ctx context.Context) Origin {
	if ctx == nil {
		return Origin{}
	}
	origin, _ := ctx.Value(originKey{}).(Origin)
	return origin
}
