package service

import "strings"

func optionalTrimmedStringPtr(raw string) *string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// optionalNonEqualStringPtr returns a pointer to value if it is non-empty and
// differs from compare; otherwise nil. Used to store upstream_model only when
// it differs from the requested model.
func optionalNonEqualStringPtr(value, compare string) *string {
	if value == "" || value == compare {
		return nil
	}
	return &value
}

func forwardResultBillingModel(requestedModel, upstreamModel string) string {
	if trimmed := strings.TrimSpace(requestedModel); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(upstreamModel)
}

// concreteForwardResultBillingModel returns the model actually sent to the
// upstream account. ForwardResult.Model is often the client or channel model,
// while UpstreamModel carries the account mapping and must win for billing.
func concreteForwardResultBillingModel(requestedModel, upstreamModel string) string {
	if trimmed := strings.TrimSpace(upstreamModel); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(requestedModel)
}

func optionalInt64Ptr(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}
