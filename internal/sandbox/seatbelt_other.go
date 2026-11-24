//go:build !darwin

package sandbox

import "errors"

// Options 保持与 macOS 实现一致，便于后续接入 Linux bubblewrap。
type Options struct {
	WorkDir string
	Network bool
}

// Available 目前只实现 macOS Seatbelt；其他平台返回明确错误，由启动流程
// 提示用户选择退出或本次不使用沙箱继续。
func Available() error {
	return errors.New("当前平台暂不支持 Seatbelt 沙箱")
}

func NewSeatbelt(Options) (Runner, error) {
	return nil, errors.New("当前平台暂不支持 Seatbelt 沙箱")
}
