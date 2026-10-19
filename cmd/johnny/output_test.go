package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

type fakeModel struct {
	stream func(func(model.Event) error) (model.Response, error)
}

func (m fakeModel) Stream(_ context.Context, _ model.Request, emit func(model.Event) error) (model.Response, error) {
	return m.stream(emit)
}

func TestStreamTextDisplaysOnlyTextAndKeepsPartialOutput(t *testing.T) {
	callErr := errors.New("调用中断")
	for _, terminalErr := range []error{nil, callErr} {
		var output bytes.Buffer
		client := fakeModel{stream: func(emit func(model.Event) error) (model.Response, error) {
			for _, event := range []model.Event{
				{Type: model.EventBlockDelta, Block: model.ContentBlock{Type: model.BlockThinking}, Delta: "隐藏的思考"},
				{Type: model.EventBlockDelta, Block: model.ContentBlock{Type: model.BlockText}, Delta: "回复"},
				{Type: model.EventBlockDelta, Block: model.ContentBlock{Type: model.BlockToolCall}, Delta: `{"path":"main.go"}`},
			} {
				if err := emit(event); err != nil {
					return model.Response{}, err
				}
			}
			return model.Response{Message: model.Message{Content: []model.ContentBlock{{Type: model.BlockText, Text: "回复"}}}}, terminalErr
		}}
		_, err := streamText(context.Background(), client, model.Request{}, &output)
		if !errors.Is(err, terminalErr) || output.String() != "回复\n" {
			t.Fatalf("显示或错误处理错误：output=%q err=%v", output.String(), err)
		}
	}
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) { return 0, nil }

func TestStreamTextPropagatesOutputFailure(t *testing.T) {
	client := fakeModel{stream: func(emit func(model.Event) error) (model.Response, error) {
		return model.Response{}, emit(model.Event{Type: model.EventBlockDelta, Block: model.ContentBlock{Type: model.BlockText}, Delta: "回复"})
	}}
	if _, err := streamText(context.Background(), client, model.Request{}, shortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("应保留输出错误链：%v", err)
	}
}
