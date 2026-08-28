package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
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

// legacyInvitePqSSLModes 是 lib/pq 认识的 sslmode 取值。
//
// 特意列出来而不是直接透传：lib/pq 支持的比 libpq 少，prefer 和 allow 都不认。
// 照着 PostgreSQL 官方文档填 prefer 是很自然的动作，但它会在 sql.Open 阶段就失败。
var legacyInvitePqSSLModes = map[string]bool{
	"disable":     true,
	"require":     true,
	"verify-ca":   true,
	"verify-full": true,
}

// validateLegacyInviteConfig 在连库之前检查配置的完整性。
//
// 这一步存在的唯一理由是让错误信息可读。DSN 里少了 user 时，lib/pq 会把下一个键值对
// 整个当成用户名，报出 `no pg_hba.conf entry for host "...", user "dbname="`——
// 从这句话根本看不出是配置漏填，只会让人去翻 pg_hba.conf。
func validateLegacyInviteConfig(cfg *config.LegacyInviteConfig) error {
	var missing []string
	if strings.TrimSpace(cfg.Host) == "" {
		missing = append(missing, "host")
	}
	if cfg.Port <= 0 {
		missing = append(missing, "port")
	}
	if strings.TrimSpace(cfg.User) == "" {
		missing = append(missing, "user")
	}
	if strings.TrimSpace(cfg.DBName) == "" {
		missing = append(missing, "dbname")
	}
	if len(missing) > 0 {
		return fmt.Errorf("缺少必填项: %s", strings.Join(missing, ", "))
	}

	// 空串留给 lib/pq 自己取默认（require），不算错。
	if mode := strings.TrimSpace(cfg.SSLMode); mode != "" && !legacyInvitePqSSLModes[mode] {
		return fmt.Errorf("sslmode=%q 不被支持，只能是 disable / require / verify-ca / verify-full", mode)
	}
	return nil
}

// logLegacyInviteDisabled 把一次「旧站库不可用」记成醒目的日志。
func logLegacyInviteDisabled(reason string, err error) {
	logger.LegacyPrintf("repository.legacy_invite",
		"[LegacyInvite] 旧站付费领码功能已自动关闭：%s：%v", reason, err)
}

// InitLegacyInviteDB 按配置建立到旧站库的连接。
//
// 功能未启用时返回 (nil, nil)：这是正常路径，不是错误。单站部署压根不需要这条链路，
// 不应该因为没填旧站配置就让整个进程起不来。
//
// 启用但配置有问题或旧站库不可达时，同样返回 (nil, nil)，只在日志里记一条错误。
// 这里刻意**不**把错误往上抛去阻断启动：领码只是一个附属入口，它连不上旧站库
// 不该拖垮整个站点。2026-08-27 上线时就是反面教材——sslmode 默认值写成了 lib/pq
// 不认的 prefer，加上漏注册三个环境变量键，Ping 失败让进程反复重启，
// 把同一版本里两个本来完全正常的功能一起打下线。
//
// 降级之后站点照常服务，领码页显示「暂未开放」，运维从日志里那条记录定位问题。
func InitLegacyInviteDB(cfg *config.Config) (*LegacyInviteDB, error) {
	if cfg == nil || !cfg.LegacyInvite.Enabled {
		return nil, nil
	}

	if err := validateLegacyInviteConfig(&cfg.LegacyInvite); err != nil {
		logLegacyInviteDisabled("配置不完整", err)
		return nil, nil
	}

	db, err := sql.Open("postgres", cfg.LegacyInvite.DSN())
	if err != nil {
		logLegacyInviteDisabled("连接参数无法解析", err)
		return nil, nil
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
		logLegacyInviteDisabled("旧站库不可达", err)
		return nil, nil
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

	return &service.LegacyPaidUser{
		UserID:     userID,
		Email:      found,
		PaidAmount: paid,
		UsageCost:  r.findUsageCost(ctx, userID),
	}, nil
}

// legacyUsageCostQuery 是第二条达标口径的数据来源：旧站的累计用量消费。
//
// 口径取 actual_cost 而非 total_cost。前者是按本站定价实际计费的金额，和用户在旧站
// 「使用统计」里看到的总消费、以及签到里「每消费满 X 解锁一次额外签到」用的是同一个字段；
// total_cost 是按标准价折算的参考值，跟用户认知里的「我花了多少」对不上。
const legacyUsageCostQuery = `
SELECT COALESCE(SUM(actual_cost), 0)::double precision
FROM usage_logs
WHERE user_id = $1`

// findUsageCost 查这个旧站用户的累计用量消费（USD）。
//
// 查询失败一律返回 0 而不是把错误往上抛：用量口径是后加的**第二条**通道，
// 旧站的只读账号很可能根本没被授予 usage_logs 的 SELECT 权限——那是一次独立的部署侧
// 授权动作（GRANT SELECT ON usage_logs TO <只读账号>），漏做是很自然的事。
// 一旦让它把错误抛上去，整个领码流程会报「暂时查不了」，等于用一条可选的新口径
// 废掉了原本工作正常的实付口径。降级成 0 之后，判定自动退回只看实付金额，
// 运维从这条日志定位授权缺口。
func (r *legacyPaidLookup) findUsageCost(ctx context.Context, userID int64) float64 {
	queryCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	var cost float64
	if err := r.db.QueryRowContext(queryCtx, legacyUsageCostQuery, userID).Scan(&cost); err != nil {
		logger.LegacyPrintf("repository.legacy_invite",
			"[LegacyInvite] 查询旧站用量消费失败，本次判定按 0 处理（检查只读账号是否有 usage_logs 的 SELECT 权限）：legacy_user_id=%d：%v",
			userID, err)
		return 0
	}
	return cost
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
SELECT id, email, legacy_user_id, paid_amount, usage_cost, redeem_code, claimed_ip, created_at
FROM legacy_invite_claims
WHERE LOWER(email) = $1
`, email).Scan(
		&claim.ID,
		&claim.Email,
		&claim.LegacyUserID,
		&claim.PaidAmount,
		&claim.UsageCost,
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
INSERT INTO legacy_invite_claims (email, legacy_user_id, paid_amount, usage_cost, redeem_code, claimed_ip)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, created_at
`, claim.Email, claim.LegacyUserID, claim.PaidAmount, claim.UsageCost, claim.RedeemCode, claim.ClaimedIP).
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
