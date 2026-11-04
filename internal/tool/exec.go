package tool

import (
	"fmt"
	"time"
)

func commandTimeout(runtime Runtime, timeoutSeconds *int) (time.Duration, error) {
	timeout := runtime.CommandTimeout
	if timeoutSeconds == nil {
		return timeout, nil
	}
	maxSeconds := int(MaxCommandTimeout / time.Second)
	if *timeoutSeconds < 1 || *timeoutSeconds > maxSeconds {
		return 0, invalidArguments(fmt.Sprintf("bash 的 timeout_seconds 必须在 1 到 %d 之间", maxSeconds), nil)
	}
	return time.Duration(*timeoutSeconds) * time.Second, nil
}
