package service

import "context"

// GroupDuplicateRepository contains the optional persistence operations needed
// by the admin group duplication workflow. It is deliberately separate from
// GroupRepository so existing repository test doubles do not gain unrelated
// methods.
type GroupDuplicateRepository interface {
	FindByDuplicateOperationID(ctx context.Context, operationID string) (*Group, error)
	CreateFromSource(ctx context.Context, duplicate *Group, sourceGroupID int64) error
}

// GroupDuplicateAdminService is an optional extension of AdminService. Keeping
// it separate avoids forcing every unrelated test double and integration seam
// to implement a new group operation.
type GroupDuplicateAdminService interface {
	DuplicateGroup(ctx context.Context, id int64, actorScope, operationKey string) (*Group, error)
	RecoverDuplicateGroup(ctx context.Context, id int64, actorScope, operationKey string) (*Group, error)
}
