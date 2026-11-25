package tool

import "strings"

// withSandboxHint 给沙箱内的失败结果补一条明确提示，避免模型只靠
// "Operation not permitted" 猜测是文件权限问题还是 Seatbelt 拦截。
func withSandboxHint(runtime Runtime, result Result) Result {
	if runtime.Sandbox == nil || !result.IsError || result.Content == "" {
		return result
	}
	if strings.Contains(result.Content, "[沙箱]") {
		return result
	}
	lower := strings.ToLower(result.Content)
	info := runtime.Sandbox.Info()

	if !info.Network && containsNetworkDenial(lower) {
		result.Content = strings.TrimRight(result.Content, "\n") +
			"\n[沙箱] 当前 Seatbelt 网络已关闭；如果这条命令需要联网，请设置 JOHNNY_SANDBOX_NETWORK=on 后重启。"
		return result
	}
	if strings.Contains(lower, "operation not permitted") {
		result.Content = strings.TrimRight(result.Content, "\n") +
			"\n[沙箱] Seatbelt 拒绝或限制了该操作：bash 只能写工作目录、临时目录和缓存目录；受限读取和 setuid 程序也可能被拦截。"
	}
	return result
}

func containsNetworkDenial(content string) bool {
	for _, marker := range []string{
		"could not resolve host",
		"couldn't connect to server",
		"could not connect",
		"network is unreachable",
		"temporary failure in name resolution",
		"nodename nor servname provided",
		"getaddrinfo",
		"enotfound",
		"eai_again",
		"etimedout",
	} {
		if strings.Contains(content, marker) {
			return true
		}
	}
	return false
}
