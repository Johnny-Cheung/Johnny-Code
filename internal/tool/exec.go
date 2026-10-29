package tool

import "time"

func commandTimeout(runtime Runtime, timeoutSeconds *int) (time.Duration, error) {
	timeout := runtime.CommandTimeout
	if timeoutSeconds == nil {
		return timeout, nil
	}
	if *timeoutSeconds < 1 || *timeoutSeconds > 600 {
		return 0, invalidArguments("bash 的 timeout_seconds 必须在 1 到 600 之间", nil)
	}
	return time.Duration(*timeoutSeconds) * time.Second, nil
}
