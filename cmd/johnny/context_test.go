package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
)

func TestContextConfigFromEnv(t *testing.T) {
	config, err := contextConfigFromEnv(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if config.Window != contextview.DefaultWindow || config.OutputTokens != contextview.DefaultOutputTokens {
		t.Fatalf("默认配置错误：%+v", config)
	}
	values := map[string]string{
		contextWindowEnv: "200000",
		maxOutputEnv:     "8192",
	}
	config, err = contextConfigFromEnv(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Window != 200_000 || config.OutputTokens != 8192 {
		t.Fatalf("覆盖配置错误：%+v", config)
	}
	if _, err := contextConfigFromEnv(func(string) (string, bool) { return "small", true }); err == nil {
		t.Fatal("非法窗口应报错")
	}
	if _, err := contextConfigFromEnv(func(key string) (string, bool) {
		if key == maxOutputEnv {
			return "999999", true
		}
		return "100000", true
	}); err == nil {
		t.Fatal("输出预留超过窗口应报错")
	}
	config, err = contextConfigFromEnv(func(key string) (string, bool) {
		if key == contextWindowEnv {
			return "32768", true
		}
		return "", false
	})
	if err != nil || config.OutputTokens != 4096 {
		t.Fatalf("最小窗口应降低默认输出预留：%+v %v", config, err)
	}
	config, err = contextConfigFromEnv(func(key string) (string, bool) {
		switch key {
		case contextWindowEnv:
			return "32768", true
		case maxOutputEnv:
			return "8192", true
		}
		return "", false
	})
	if err != nil || config.OutputTokens != 8192 {
		t.Fatalf("显式输出预留应被尊重：%+v %v", config, err)
	}
}

func TestRequestTimeoutFromEnv(t *testing.T) {
	timeout, err := requestTimeoutFromEnv(func(string) (string, bool) { return "", false })
	if err != nil || timeout != agent.DefaultRequestTimeout {
		t.Fatalf("默认请求超时错误：%v %v", timeout, err)
	}
	timeout, err = requestTimeoutFromEnv(func(key string) (string, bool) {
		if key == requestTimeoutEnv {
			return "90m", true
		}
		return "", false
	})
	if err != nil || timeout != 90*time.Minute {
		t.Fatalf("请求超时覆盖错误：%v %v", timeout, err)
	}
	for _, value := range []string{"0", "-1m", "abc", ""} {
		value := value
		if _, err := requestTimeoutFromEnv(func(string) (string, bool) { return value, true }); err == nil {
			t.Fatalf("非法请求超时应报错：%q", value)
		}
	}
}

func TestContextStatsText(t *testing.T) {
	stats := contextview.Stats{
		Budget:         contextview.NewBudget(200_000, 4096),
		Input:          10_000,
		SystemTokens:   1_000,
		ToolTokens:     500,
		MessageTokens:  8_500,
		Turns:          3,
		SummaryTurns:   2,
		CoveredThrough: 2,
		Summary:        "旧摘要",
	}
	text := contextStatsText(stats)
	for _, want := range []string{"上下文估算", "10000", "系统 1000", "工具定义 500", "消息 8500", "覆盖到第 2 轮", "旧摘要"} {
		if !strings.Contains(text, want) {
			t.Fatalf("上下文统计缺少 %q：%s", want, text)
		}
	}
}

func TestLocalContextCommands(t *testing.T) {
	controller := &contextController{
		stats: func() string { return "上下文状态" },
		compact: func(context.Context) (string, error) {
			return "已压缩", nil
		},
	}
	handler := newLocalCommandHandler(nil, nil, nil, controller)
	result, handled := handler.Handle("/context")
	if !handled || result.Output != "上下文状态" {
		t.Fatalf("/context 处理错误：%+v %v", result, handled)
	}
	result, handled = handler.Handle("/compact")
	if !handled || !result.Compact {
		t.Fatalf("/compact 应请求后台压缩：%+v %v", result, handled)
	}
	result, handled = handler.Handle("/context extra")
	if !handled || !strings.Contains(result.Output, "用法") {
		t.Fatalf("/context 多余参数应显示用法：%+v %v", result, handled)
	}
}
