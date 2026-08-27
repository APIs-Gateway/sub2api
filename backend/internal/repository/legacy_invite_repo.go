package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/lib/pq"
)

// LegacyInviteDB 是到旧站数据库的只读连接。
//
// 单独起一个类型而不是直接用 *sql.DB，是为了让 wire 能把它和主库的 *sql.DB 区分开——
// 两者类型相同但语义完全不同，混淆的后果是把查询打到错误的库上。
type LegacyInviteDB struct {
	*sql.DB
}

// InitLegacyInviteDB 按配置建立到旧站库的连接。
//
// 功能未启用时返回 (nil, nil)：这是正常路径，不是错误。单站部署压根不需要这条链路，
// 不应该因为没填旧站配置就让整个进程起不来。
//
// 启用时会立刻 Ping 一次做快速失败：配置写错要在启动阶段就暴露，
// 而不是等到第一个用户来领码才发现连不上。
func InitLegacyInviteDB(cfg *config.Config) (*LegacyInviteDB, error) {
	if cfg == nil || !cfg.LegacyInvite.Enabled {
		return nil, nil
	}

	db, err := sql.Open("postgres", cfg.LegacyInvite.DSN())
	if err != nil {
		return nil, fmt.Errorf("open legacy site database: %w", err)
	}

	maxConns := cfg.LegacyInvite.MaxOpenConns
	if maxConns <= 0 {
		maxConns = 4
	}
	db.SetMaxOpenConns(maxConns)
	// 空闲连接留一个即可：领码是低频操作，长期占着旧站的连接位没有意义。
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), legacyInviteQueryTimeout(cfg))
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping legacy site database: %w", err)
	}

	return &LegacyInviteDB{DB: db}, nil
}

// legacyInviteQueryTimeout 返回单次跨库查询的超时时间。
func legacyInviteQueryTimeout(cfg *config.Config) time.Duration {
	seconds := 5
	if cfg != nil && cfg.LegacyInvite.QueryTimeoutSeconds > 0 {
		seconds = cfg.LegacyInvite.QueryTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

type legacyPaidLookup struct {
	db      *sql.DB
	timeout time.Duration
}

// NewLegacyPaidLookup 构造旧站付费画像的只读查询器。
// db 为 nil（功能未启用）时返回 nil 接口值，上层据此判定功能不可用。
func NewLegacyPaidLookup(db *LegacyInviteDB, cfg *config.Config) service.LegacyPaidLookup {
	if db == nil || db.DB == nil {
		// 必须显式返回 nil 而不是包了 nil 指针的接口值，
		// 否则上层的 `lookup != nil` 会误判成「已配置」。
		return nil
	}
	return &legacyPaidLookup{db: db.DB, timeout: legacyInviteQueryTimeout(cfg)}
}

// legacyPaidStatuses 是计入实付金额的订单状态。
//
// 只认已完成的单：CANCELLED / EXPIRED / FAILED / PENDING 都没真正付钱。
// 全额和部分退款的单仍然计入，因为要用 pay_amount 减去 refund_amount 得到净付出，
// 直接排除掉会让「充了又退」和「从没充过」变成同一件事。
var legacyPaidStatuses = pq.Array([]string{"COMPLETED", "PARTIALLY_REFUNDED", "REFUNDED"})

// FindPaidUser 查询该邮箱在旧站的净实付金额。
//
// 口径：pay_amount（实付人民币）合计，减去 refund_amount（已退款）。
// 特意不用 amount 字段——旧站的余额充值单里 amount 是到账额度而非付款金额，
// 两者相差一个赠送倍率，拿它当「花了多少钱」会大幅高估。
func (r *legacyPaidLookup) FindPaidUser(ctx context.Context, email string) (*service.LegacyPaidUser, error) {
	queryCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	var (
		userID int64
		found  string
		paid   float64
	)
	err := r.db.QueryRowContext(queryCtx, `
SELECT u.id,
       COALESCE(u.email, ''),
       COALESCE(SUM(p.pay_amount - COALESCE(p.refund_amount, 0)), 0)::double precision AS paid
FROM users u
LEFT JOIN payment_orders p
       ON p.user_id = u.id
      AND p.status = ANY($2)
WHERE LOWER(u.email) = $1
GROUP BY u.id, u.email
`, email, legacyPaidStatuses).Scan(&userID, &found, &paid)
	if errors.Is(err, sql.ErrNoRows) {
		// 旧站没有这个邮箱。不是错误——上层会当作「不达标」处理。
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query legacy paid user: %w", err)
	}

	return &service.LegacyPaidUser{UserID: userID, Email: found, PaidAmount: paid}, nil
}

// Ping 检查旧站库是否可达。
func (r *legacyPaidLookup) Ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	return r.db.PingContext(pingCtx)
}

type legacyInviteClaimRepository struct {
	db *sql.DB
}

// NewLegacyInviteClaimRepository 基于主库记录领码流水。
// 注意这里用的是**本站**主库，与上面的旧站只读连接不是一回事。
func NewLegacyInviteClaimRepository(db *sql.DB) service.LegacyInviteClaimRepository {
	return &legacyInviteClaimRepository{db: db}
}

func (r *legacyInviteClaimRepository) GetByEmail(ctx context.Context, email string) (*service.LegacyInviteClaim, error) {
	var claim service.LegacyInviteClaim
	err := r.db.QueryRowContext(ctx, `
SELECT id, email, legacy_user_id, paid_amount, redeem_code, claimed_ip, created_at
FROM legacy_invite_claims
WHERE LOWER(email) = $1
`, email).Scan(
		&claim.ID,
		&claim.Email,
		&claim.LegacyUserID,
		&claim.PaidAmount,
		&claim.RedeemCode,
		&claim.ClaimedIP,
		&claim.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query legacy invite claim: %w", err)
	}
	return &claim, nil
}

func (r *legacyInviteClaimRepository) Create(ctx context.Context, claim *service.LegacyInviteClaim) error {
	if claim == nil {
		return errors.New("legacy invite claim is nil")
	}
	err := r.db.QueryRowContext(ctx, `
INSERT INTO legacy_invite_claims (email, legacy_user_id, paid_amount, redeem_code, claimed_ip)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, created_at
`, claim.Email, claim.LegacyUserID, claim.PaidAmount, claim.RedeemCode, claim.ClaimedIP).
		Scan(&claim.ID, &claim.CreatedAt)
	if err != nil {
		// lower(email) 上的唯一索引是「每人一个码」的最终闸门：
		// 并发的第二个请求会撞在这里，翻译成业务错误让上层把先前那个码还回去。
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			return service.ErrLegacyInviteAlreadyClaimed
		}
		return fmt.Errorf("insert legacy invite claim: %w", err)
	}
	return nil
}
