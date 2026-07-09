package service

import "context"

type openAIAllGroupsRoutingContextKey struct{}

// WithOpenAIAllGroupsRouting marks an OpenAI request to route over every
// schedulable OpenAI account, regardless of the API key's selected group.
func WithOpenAIAllGroupsRouting(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIAllGroupsRoutingContextKey{}, true)
}

func openAIAllGroupsRoutingEnabled(ctx context.Context) bool {
	enabled, _ := ctx.Value(openAIAllGroupsRoutingContextKey{}).(bool)
	return enabled
}

func openAIStickyRoutingGroupID(groupID *int64, allGroupsRouting bool) *int64 {
	if allGroupsRouting {
		return nil
	}
	return groupID
}
