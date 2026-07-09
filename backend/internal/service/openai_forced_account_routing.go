package service

import "context"

type openAIForcedAccountRoutingContextKey struct{}

func WithOpenAIForcedAccountRouting(ctx context.Context, accountID int64) context.Context {
	if ctx == nil || accountID <= 0 {
		return ctx
	}
	return context.WithValue(ctx, openAIForcedAccountRoutingContextKey{}, accountID)
}

func openAIForcedAccountRoutingID(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	accountID, _ := ctx.Value(openAIForcedAccountRoutingContextKey{}).(int64)
	if accountID <= 0 {
		return 0
	}
	return accountID
}
