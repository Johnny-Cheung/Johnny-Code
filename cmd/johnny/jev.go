package main

import (
	"context"

	"github.com/Johnny-Cheung/Johnny-Code/internal/jev"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

// jevClassifier 把 JEV 客户端适配成工具层的最小分类接口。
type jevClassifier struct {
	client *jev.Client
}

func (c *jevClassifier) Classify(ctx context.Context, command, workDir string) (tool.Classification, error) {
	result, err := c.client.Classify(ctx, command, workDir)
	if err != nil {
		return tool.Classification{}, err
	}
	return tool.Classification{
		Decision:      tool.ClassificationDecision(result.Decision),
		Probabilities: result.Probabilities,
		Reason:        result.Reason,
		Model:         result.Model,
	}, nil
}
