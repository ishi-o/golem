// Package mocks contains generated test doubles shared by the central test module.
package mocks

import (
	"context"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ToolCallingChatModel is the model contract used by the agent tests.
//
// It is kept local so mock generation remains independent of changes in the
// framework's source layout while the generated mock still implements Eino's
// public interface.
type ToolCallingChatModel interface {
	Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error)
	Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error)
	WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error)
}

//go:generate go run go.uber.org/mock/mockgen -source generate.go -destination model.go -package mocks
