package repository

import (
	"context"
	"fmt"
	"math"

	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// DeductRefundBalance atomically claws back only available wallet credit.
// The balance predicate makes the read/modify/write safe against concurrent
// usage and refunds on all Ent dialects. Ordinary usage keeps DeductBalance's
// deliberate overdraft policy. An ambient settlement transaction owns commit.
func (r *userRepository) DeductRefundBalance(ctx context.Context, id int64, amount float64, allowPartial bool) (float64, error) {
	if id <= 0 || amount <= 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return 0, fmt.Errorf("invalid refund balance deduction")
	}
	client := clientFromContext(ctx, r.client)
	for attempt := 0; attempt < 16; attempt++ {
		user, err := client.User.Query().Where(dbuser.IDEQ(id), dbuser.DeletedAtIsNil()).Select(dbuser.FieldID, dbuser.FieldBalance).Only(ctx)
		if err != nil {
			return 0, translatePersistenceError(err, service.ErrUserNotFound, nil)
		}
		if math.IsNaN(user.Balance) || math.IsInf(user.Balance, 0) {
			return 0, fmt.Errorf("invalid stored refund balance")
		}
		available := math.Max(user.Balance, 0)
		if !allowPartial && available < amount {
			return 0, service.ErrRefundBalanceInsufficient
		}
		deducted := math.Min(available, amount)
		if deducted == 0 {
			return 0, nil
		}
		updated, err := client.User.Update().
			Where(dbuser.IDEQ(id), dbuser.DeletedAtIsNil(), dbuser.BalanceEQ(user.Balance)).
			SetBalance(user.Balance - deducted).
			Save(ctx)
		if err != nil {
			return 0, err
		}
		if updated == 1 {
			return deducted, nil
		}
	}
	return 0, fmt.Errorf("refund balance changed repeatedly; retry settlement")
}
