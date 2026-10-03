package service

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
)

// BillingInflightRepository is deliberately separate from the billing interface:
// older test doubles and deployments without the new migration fail open.
type BillingInflightRepository interface {
	ReserveBillingInflight(context.Context, int64, string, float64, bool, time.Duration) (bool, error)
	ResizeBillingInflight(context.Context, int64, string, string, float64, bool, time.Duration) (bool, error)
	RenewBillingInflight(context.Context, int64, string, time.Duration) (bool, error)
	ReleaseBillingInflight(context.Context, int64, string) error
	StageBillingInflight(context.Context, int64, string, string, *UsageBillingCommand, time.Duration) (string, error)
	FinishBillingInflightAttempt(context.Context, int64, string, string) error
}

type billingInflightContextKey struct{}
type billingInflightTaskKey struct{}

// BillingInflightLease spans the logical request and its queued usage tasks.
// Only successful settlement/no-charge proof releases a task's hold early.
// Dropped/failed tasks keep a bounded lease; this is not a durable billing queue.
type BillingInflightLease struct {
	repo        BillingInflightRepository
	userID      int64
	id          string
	attemptID   string
	attempts    map[string]*billingInflightAttempt
	ttl         time.Duration
	mu          sync.Mutex
	refs        int
	failed      bool
	handlerDone bool
	dispatched  bool
	hadTasks    bool
	stop        chan struct{}
	stopped     sync.Once
	released    sync.Once
}

type billingInflightAttempt struct {
	tasks, priced                int
	closed, noCharge, dispatched bool
}

func WithBillingInflightLease(ctx context.Context, lease *BillingInflightLease) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, billingInflightContextKey{}, lease)
}

func BillingInflightLeaseFromContext(ctx context.Context) *BillingInflightLease {
	if ctx == nil {
		return nil
	}
	lease, _ := ctx.Value(billingInflightContextKey{}).(*BillingInflightLease)
	return lease
}

func newBillingInflightLease(ctx context.Context, repo UsageBillingRepository, cfg *config.Config, userID int64, amount float64, exclusive bool) (*BillingInflightLease, error) {
	if cfg == nil || !cfg.Billing.InflightReservation.Enabled || cfg.RunMode == config.RunModeSimple {
		return nil, nil
	}
	backend, ok := repo.(BillingInflightRepository)
	if !ok || backend == nil {
		return nil, nil
	}
	ttl := time.Duration(cfg.Billing.InflightReservation.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	lease := &BillingInflightLease{repo: backend, userID: userID, id: uuid.NewString(), ttl: ttl, refs: 1, stop: make(chan struct{})}
	lease.attemptID = lease.id + ":initial"
	lease.attempts = map[string]*billingInflightAttempt{lease.attemptID: {}}
	allowed, err := backend.ReserveBillingInflight(ctx, userID, lease.id, amount, exclusive, ttl)
	if err != nil {
		if billingInflightDenial(err) {
			return nil, err
		}
		logger.LegacyPrintf("service.billing_inflight", "Warning: admission reservation failed open user=%d: %v", userID, err)
		return nil, nil
	}
	if !allowed {
		return nil, ErrInsufficientBalance
	}
	go lease.renew()
	return lease, nil
}

func (l *BillingInflightLease) Resize(ctx context.Context, amount float64, exclusive bool) error {
	if l == nil {
		return nil
	}
	l.closeAttempt()
	attemptID := uuid.NewString()
	allowed, err := l.repo.ResizeBillingInflight(ctx, l.userID, l.id, attemptID, amount, exclusive, l.ttl)
	if err != nil {
		if billingInflightDenial(err) {
			return err
		}
		logger.LegacyPrintf("service.billing_inflight", "Warning: reservation resize failed open user=%d: %v", l.userID, err)
		return nil
	}
	if !allowed {
		return ErrInsufficientBalance
	}
	l.mu.Lock()
	l.attemptID = attemptID
	l.attempts[attemptID] = &billingInflightAttempt{}
	l.mu.Unlock()
	return nil
}

func (l *BillingInflightLease) renew() {
	interval := l.ttl / 3
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			alive, err := l.repo.RenewBillingInflight(ctx, l.userID, l.id, l.ttl)
			cancel()
			if err != nil {
				logger.LegacyPrintf("service.billing_inflight", "Warning: reservation renewal failed user=%d: %v", l.userID, err)
			}
			if err == nil && !alive {
				return
			}
		}
	}
}

func (l *BillingInflightLease) HandlerDone() {
	if l == nil {
		return
	}
	l.stopped.Do(func() { close(l.stop) })
	l.mu.Lock()
	if l.handlerDone {
		l.mu.Unlock()
		return
	}
	l.handlerDone = true
	var readyAttempts []string
	for id, attempt := range l.attempts {
		attempt.closed = true
		if attempt.dispatched && attempt.tasks == 0 && !attempt.noCharge {
			l.failed = true
		}
		if attempt.noCharge || (attempt.tasks > 0 && attempt.tasks == attempt.priced) {
			readyAttempts = append(readyAttempts, id)
		}
	}
	l.refs--
	release := l.refs == 0 && !l.failed
	l.mu.Unlock()
	for _, id := range readyAttempts {
		l.finishAttempt(id)
	}
	if release {
		l.release()
	}
}

func (l *BillingInflightLease) release() {
	l.released.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := l.repo.ReleaseBillingInflight(ctx, l.userID, l.id); err != nil {
			logger.LegacyPrintf("service.billing_inflight", "Warning: reservation release failed (bounded TTL) user=%d: %v", l.userID, err)
		}
	})
}

type billingInflightTask struct {
	lease     *BillingInflightLease
	attemptID string
	mu        sync.Mutex
	completed bool
	priced    bool
	once      sync.Once
}

// AcquireBillingInflightTask must run before enqueue, not when the worker starts.
// finish(false) is used for a dropped task; finish(true) requires an explicit
// success marker from the cost/settlement path, rather than a normal function return.
func AcquireBillingInflightTask(parent context.Context) (func(context.Context) context.Context, func(bool)) {
	l := BillingInflightLeaseFromContext(parent)
	if l == nil {
		return func(ctx context.Context) context.Context { return ctx }, func(bool) {}
	}
	l.mu.Lock()
	l.refs++
	l.hadTasks = true
	attemptID := l.attemptID
	l.attempts[attemptID].tasks++
	l.mu.Unlock()
	task := &billingInflightTask{lease: l, attemptID: attemptID}
	return func(ctx context.Context) context.Context {
			ctx = WithBillingInflightLease(ctx, l)
			return context.WithValue(ctx, billingInflightTaskKey{}, task)
		}, func(executed bool) {
			task.once.Do(func() {
				task.mu.Lock()
				complete := task.completed
				task.mu.Unlock()
				l.mu.Lock()
				if !executed || !complete {
					l.failed = true
				}
				l.refs--
				release := l.refs == 0 && l.handlerDone && !l.failed
				l.mu.Unlock()
				if release {
					l.release()
				}
			})
		}
}

func CompleteBillingInflightTask(ctx context.Context) {
	if ctx == nil {
		return
	}
	markBillingInflightCostKnown(ctx)
	if task, ok := ctx.Value(billingInflightTaskKey{}).(*billingInflightTask); ok && task != nil {
		task.mu.Lock()
		task.completed = true
		task.mu.Unlock()
	}
}

func stageBillingInflight(ctx context.Context, cmd *UsageBillingCommand) {
	l := BillingInflightLeaseFromContext(ctx)
	if l == nil || cmd == nil {
		return
	}
	l.mu.Lock()
	attemptID := l.attemptID
	l.mu.Unlock()
	if task, ok := ctx.Value(billingInflightTaskKey{}).(*billingInflightTask); ok && task != nil {
		attemptID = task.attemptID
	}
	if cmd.BalanceCost <= 0 && cmd.OfficialCost <= 0 {
		markBillingInflightCostKnown(ctx)
		return
	}
	id, err := l.repo.StageBillingInflight(ctx, l.userID, l.id, attemptID, cmd, l.ttl)
	if err != nil {
		logger.LegacyPrintf("service.billing_inflight", "Warning: known-cost reservation failed open user=%d: %v", l.userID, err)
		return
	}
	cmd.InflightObligationID = id
	markBillingInflightCostKnown(ctx)
}

func finiteBillingInflightAmount(amount float64) bool {
	return amount >= 0 && !math.IsNaN(amount) && !math.IsInf(amount, 0)
}

var ErrBillingInflightIdentity = errors.New("billing inflight obligation identity conflict")

func (l *BillingInflightLease) MarkDispatched() {
	if l != nil {
		l.mu.Lock()
		l.dispatched = true
		l.attempts[l.attemptID].dispatched = true
		l.mu.Unlock()
	}
}

func (l *BillingInflightLease) closeAttempt() {
	l.mu.Lock()
	id := l.attemptID
	attempt := l.attempts[id]
	attempt.closed = true
	ready := attempt.noCharge || (attempt.tasks > 0 && attempt.priced == attempt.tasks)
	l.mu.Unlock()
	if ready {
		l.finishAttempt(id)
	}
}

func (l *BillingInflightLease) finishAttempt(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := l.repo.FinishBillingInflightAttempt(ctx, l.userID, l.id, id); err != nil {
		logger.LegacyPrintf("service.billing_inflight", "Warning: attempt release failed (bounded TTL) user=%d: %v", l.userID, err)
	}
}

// Call only for a safe pre-output failover with nil result and no separately
// registered financial task. Transport/unknown errors and dropped tasks retain TTL.
func MarkBillingInflightAttemptNoCharge(ctx context.Context) {
	l := BillingInflightLeaseFromContext(ctx)
	if l == nil {
		return
	}
	l.mu.Lock()
	id := l.attemptID
	attempt := l.attempts[id]
	safe := attempt.tasks == 0
	if safe {
		attempt.noCharge = true
		attempt.closed = true
	}
	l.mu.Unlock()
	if safe {
		l.finishAttempt(id)
	}
}

func markBillingInflightCostKnown(ctx context.Context) {
	task, ok := ctx.Value(billingInflightTaskKey{}).(*billingInflightTask)
	if !ok || task == nil {
		return
	}
	task.mu.Lock()
	if task.priced {
		task.mu.Unlock()
		return
	}
	task.priced = true
	task.mu.Unlock()
	l := task.lease
	l.mu.Lock()
	attempt := l.attempts[task.attemptID]
	attempt.priced++
	ready := attempt.closed && attempt.priced == attempt.tasks
	l.mu.Unlock()
	if ready {
		l.finishAttempt(task.attemptID)
	}
}

func billingInflightDenial(err error) bool {
	return errors.Is(err, ErrInsufficientBalance) || errors.Is(err, ErrDailyLimitExceeded) || errors.Is(err, ErrWeeklyLimitExceeded) || errors.Is(err, ErrMonthlyLimitExceeded)
}
