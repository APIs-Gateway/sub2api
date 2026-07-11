//go:build unit

package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

// 邀请返利积分制（issue #11，方案 C）—— earning 编排的 service 层单测。
// 覆盖 AccrueEarnForRedeem（兑换码返积分）与 AccrueEarnForOrder（法币订单返积分）的
// 比例解析（全局/专属覆盖）、base 解析（面值/订阅返利基数）、各类守卫与幂等契约。
// 用纯 fake 拼装真实 SettingService + AffiliateService，无需 DB。

// --- fake: SettingRepository（只读 points 设置） ---

type pointsEarnSettingRepo struct{ values map[string]string }

func (r *pointsEarnSettingRepo) Get(ctx context.Context, key string) (*Setting, error) {
	panic("unexpected Get call")
}
func (r *pointsEarnSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	if v, ok := r.values[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}
func (r *pointsEarnSettingRepo) Set(ctx context.Context, key, value string) error {
	panic("unexpected Set call")
}
func (r *pointsEarnSettingRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	panic("unexpected GetMultiple call")
}
func (r *pointsEarnSettingRepo) SetMultiple(ctx context.Context, settings map[string]string) error {
	panic("unexpected SetMultiple call")
}
func (r *pointsEarnSettingRepo) GetAll(ctx context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}
func (r *pointsEarnSettingRepo) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete call")
}

// --- fake: AffiliateRepository (+ AffiliateCashbackRepository) ---

type pointsEarnAffiliateRepo struct {
	summaries map[int64]*AffiliateSummary // userID → summary（InviterID / AffRebateRatePercent）
	ensureErr map[int64]error             // 可选：对特定 userID 注入错误
	subBase   map[string]float64          // "groupID:days" → 订阅返利 base（balance 单位）
	ensureN   int                         // EnsureUserAffiliate 调用计数
	subBaseN  int                         // GetSubscriptionCashbackBaseAmount 调用计数
}

func (r *pointsEarnAffiliateRepo) EnsureUserAffiliate(ctx context.Context, userID int64) (*AffiliateSummary, error) {
	r.ensureN++
	if r.ensureErr != nil {
		if err := r.ensureErr[userID]; err != nil {
			return nil, err
		}
	}
	if s, ok := r.summaries[userID]; ok {
		return s, nil
	}
	return &AffiliateSummary{UserID: userID}, nil
}

func (r *pointsEarnAffiliateRepo) GetSubscriptionCashbackBaseAmount(ctx context.Context, groupID int64, validityDays int) (float64, bool, error) {
	r.subBaseN++
	if v, ok := r.subBase[fmt.Sprintf("%d:%d", groupID, validityDays)]; ok {
		return v, true, nil
	}
	return 0, false, nil
}

func (r *pointsEarnAffiliateRepo) GetAffiliateByCode(ctx context.Context, code string) (*AffiliateSummary, error) {
	panic("unexpected GetAffiliateByCode call")
}
func (r *pointsEarnAffiliateRepo) BindInviter(ctx context.Context, userID, inviterID int64) (bool, error) {
	panic("unexpected BindInviter call")
}
func (r *pointsEarnAffiliateRepo) ListInvitees(ctx context.Context, inviterID int64, limit int) ([]AffiliateInvitee, error) {
	panic("unexpected ListInvitees call")
}
func (r *pointsEarnAffiliateRepo) UpdateUserAffCode(ctx context.Context, userID int64, newCode string) error {
	panic("unexpected UpdateUserAffCode call")
}
func (r *pointsEarnAffiliateRepo) ResetUserAffCode(ctx context.Context, userID int64) (string, error) {
	panic("unexpected ResetUserAffCode call")
}
func (r *pointsEarnAffiliateRepo) SetUserRebateRate(ctx context.Context, userID int64, ratePercent *float64) error {
	panic("unexpected SetUserRebateRate call")
}
func (r *pointsEarnAffiliateRepo) BatchSetUserRebateRate(ctx context.Context, userIDs []int64, ratePercent *float64) error {
	panic("unexpected BatchSetUserRebateRate call")
}
func (r *pointsEarnAffiliateRepo) ListUsersWithCustomSettings(ctx context.Context, filter AffiliateAdminFilter) ([]AffiliateAdminEntry, int64, error) {
	panic("unexpected ListUsersWithCustomSettings call")
}
func (r *pointsEarnAffiliateRepo) ListAffiliateInviteRecords(ctx context.Context, filter AffiliateRecordFilter) ([]AffiliateInviteRecord, int64, error) {
	panic("unexpected ListAffiliateInviteRecords call")
}
func (r *pointsEarnAffiliateRepo) ListAffiliateRebateRecords(ctx context.Context, filter AffiliateRecordFilter) ([]AffiliateRebateRecord, int64, error) {
	panic("unexpected ListAffiliateRebateRecords call")
}
func (r *pointsEarnAffiliateRepo) GetAffiliateUserOverview(ctx context.Context, userID int64) (*AffiliateUserOverview, error) {
	panic("unexpected GetAffiliateUserOverview call")
}
func (r *pointsEarnAffiliateRepo) ListCashbackSubscriptionMappings(ctx context.Context) ([]AffiliateCashbackSubscriptionMapping, error) {
	panic("unexpected ListCashbackSubscriptionMappings call")
}
func (r *pointsEarnAffiliateRepo) ReplaceCashbackSubscriptionMappings(ctx context.Context, entries []AffiliateCashbackSubscriptionMapping) error {
	panic("unexpected ReplaceCashbackSubscriptionMappings call")
}
func (r *pointsEarnAffiliateRepo) ListCashbackRecords(ctx context.Context, filter AffiliateRecordFilter) ([]AffiliateCashbackRecord, int64, error) {
	panic("unexpected ListCashbackRecords call")
}
func (r *pointsEarnAffiliateRepo) ListUserCashbackRecords(ctx context.Context, userID int64, limit int) ([]AffiliateCashbackRecord, error) {
	panic("unexpected ListUserCashbackRecords call")
}
func (r *pointsEarnAffiliateRepo) GetUserCashbackTotal(ctx context.Context, userID int64) (float64, error) {
	panic("unexpected GetUserCashbackTotal call")
}

// --- fake: PointsRepository（只实现 EarnPoints，按来源锚模拟 partial-unique 幂等） ---

type pointsEarnRepo struct {
	calls    []EarnPointsInput
	seen     map[string]bool
	forceErr error
}

func (r *pointsEarnRepo) EarnPoints(ctx context.Context, in EarnPointsInput) (bool, error) {
	r.calls = append(r.calls, in)
	if r.forceErr != nil {
		return false, r.forceErr
	}
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	// 模拟 (user_id, source_*) WHERE kind='earn' 的 partial-unique：同来源重放不重复入账。
	key := fmt.Sprintf("%d|o%d|r%d", in.InviterID, in.SourceOrderID, in.SourceRedeemCodeID)
	if r.seen[key] {
		return false, nil
	}
	r.seen[key] = true
	return true, nil
}

func (r *pointsEarnRepo) EnsureAccount(ctx context.Context, userID int64) (*PointsAccount, error) {
	panic("unexpected EnsureAccount call")
}
func (r *pointsEarnRepo) GetAccount(ctx context.Context, userID int64) (*PointsAccount, error) {
	panic("unexpected GetAccount call")
}
func (r *pointsEarnRepo) ClawbackByOrder(ctx context.Context, sourceOrderID int64, refundAmount, originalAmount float64) (int64, error) {
	panic("unexpected ClawbackByOrder call")
}
func (r *pointsEarnRepo) ThawDuePoints(ctx context.Context, userID int64) (int64, error) {
	panic("unexpected ThawDuePoints call")
}
func (r *pointsEarnRepo) RedeemToBalance(ctx context.Context, userID, points int64, balanceDelta, pegAt float64) (float64, error) {
	panic("unexpected RedeemToBalance call")
}
func (r *pointsEarnRepo) DeductForPlan(ctx context.Context, userID, points int64, pegAt float64, note, idempotencyKey string) error {
	panic("unexpected DeductForPlan call")
}
func (r *pointsEarnRepo) CreateWithdrawal(ctx context.Context, in CreateWithdrawalInput) (*PointsWithdrawal, error) {
	panic("unexpected CreateWithdrawal call")
}
func (r *pointsEarnRepo) GetWithdrawal(ctx context.Context, id int64) (*PointsWithdrawal, error) {
	panic("unexpected GetWithdrawal call")
}
func (r *pointsEarnRepo) ReviewWithdrawal(ctx context.Context, id, adminID int64, approve bool, note, payoutProof string) (*PointsWithdrawal, error) {
	panic("unexpected ReviewWithdrawal call")
}
func (r *pointsEarnRepo) ListUserWithdrawals(ctx context.Context, userID int64, limit int) ([]PointsWithdrawal, error) {
	panic("unexpected ListUserWithdrawals call")
}
func (r *pointsEarnRepo) ListUserLedger(ctx context.Context, userID int64, page, pageSize int) ([]PointsLedgerEntry, int64, error) {
	panic("unexpected ListUserLedger call")
}
func (r *pointsEarnRepo) ListWithdrawals(ctx context.Context, filter PointsWithdrawalFilter) ([]PointsWithdrawal, int64, error) {
	panic("unexpected ListWithdrawals call")
}
func (r *pointsEarnRepo) ListLedger(ctx context.Context, filter PointsLedgerFilter) ([]PointsLedgerEntry, int64, error) {
	panic("unexpected ListLedger call")
}

// --- 构造测试用 PointsService ---

// pointsEarnDefaults 默认设置：启用、peg=0.01、返现率 20%、冻结 0h。
func pointsEarnDefaults() map[string]string {
	return map[string]string{
		SettingKeyPointsEnabled:      "true",
		SettingKeyPointsPeg:          "0.01",
		SettingKeyPointsCashbackRate: "20",
		SettingKeyPointsFreezeHours:  "0",
	}
}

func newEarnPointsService(settings map[string]string, aff *pointsEarnAffiliateRepo, prepo *pointsEarnRepo) *PointsService {
	setSvc := &SettingService{settingRepo: &pointsEarnSettingRepo{values: settings}}
	affSvc := NewAffiliateService(aff, setSvc, nil, nil)
	return &PointsService{
		repo:             prepo,
		settingService:   setSvc,
		affiliateService: affSvc,
	}
}

// inviteePair 造一对「邀请人 ← 被邀请人」身份图谱；override!=nil 时给邀请人设专属返现率。
func inviteePair(inviteeID, inviterID int64, override *float64) map[int64]*AffiliateSummary {
	return map[int64]*AffiliateSummary{
		inviteeID: {UserID: inviteeID, InviterID: &inviterID},
		inviterID: {UserID: inviterID, AffRebateRatePercent: override},
	}
}

func balanceCode(id int64, value float64) *RedeemCode {
	return &RedeemCode{ID: id, Type: RedeemTypeBalance, Value: value}
}

func subscriptionCode(id, groupID int64, days int) *RedeemCode {
	g := groupID
	return &RedeemCode{ID: id, Type: RedeemTypeSubscription, GroupID: &g, ValidityDays: days}
}

func paymentOrder(userID int64, amount float64) *dbent.PaymentOrder {
	return &dbent.PaymentOrder{ID: 555, UserID: userID, Amount: amount}
}

func newPointsEarnEntClient(t *testing.T) *dbent.Client {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", name))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func createPointsEarnUser(t *testing.T, client *dbent.Client, email string) *dbent.User {
	t.Helper()
	user, err := client.User.Create().
		SetEmail(email).
		SetPasswordHash("hash").
		SetUsername(email).
		Save(context.Background())
	require.NoError(t, err)
	return user
}

func createPointsEarnOrder(t *testing.T, client *dbent.Client, user *dbent.User, status string, amount float64) *dbent.PaymentOrder {
	t.Helper()
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(amount).
		SetPayAmount(amount).
		SetRechargeCode(fmt.Sprintf("code-%d-%s", user.ID, status)).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo(fmt.Sprintf("trade-%d-%s", user.ID, status)).
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(status).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("localhost").
		Save(context.Background())
	require.NoError(t, err)
	return order
}

// ============================ AccrueEarnForRedeem ============================

func TestAccrueEarnForRedeem_BalanceCode_NoEarn(t *testing.T) {
	t.Parallel()
	aff := &pointsEarnAffiliateRepo{summaries: inviteePair(2, 1, nil)}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(pointsEarnDefaults(), aff, prepo)

	pts, err := svc.AccrueEarnForRedeem(context.Background(), 2, balanceCode(77, 100))
	require.NoError(t, err)
	require.EqualValues(t, 0, pts, "balance 兑换码可能是赠码/批量码，不默认返邀请积分")
	require.Empty(t, prepo.calls)
	require.Zero(t, aff.ensureN, "balance 码不应触碰身份图谱")
}

func TestAccrueEarnForRedeem_SubscriptionCode_BaseFromCashback(t *testing.T) {
	t.Parallel()
	aff := &pointsEarnAffiliateRepo{
		summaries: inviteePair(2, 1, nil),
		subBase:   map[string]float64{"7:30": 50},
	}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(pointsEarnDefaults(), aff, prepo)

	// 订阅码 base 取返利基数 50 → floor(50 × 20% / 0.01) = 1000。
	pts, err := svc.AccrueEarnForRedeem(context.Background(), 2, subscriptionCode(88, 7, 30))
	require.NoError(t, err)
	require.EqualValues(t, 1000, pts)
	require.Equal(t, 1, aff.subBaseN, "走订阅返利基数解析")
	require.Len(t, prepo.calls, 1)
	require.EqualValues(t, 88, prepo.calls[0].SourceRedeemCodeID)
}

func TestAccrueEarnForRedeem_SubscriptionCode_BaseNotFound(t *testing.T) {
	t.Parallel()
	aff := &pointsEarnAffiliateRepo{summaries: inviteePair(2, 1, nil)} // 无 subBase 映射
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(pointsEarnDefaults(), aff, prepo)

	pts, err := svc.AccrueEarnForRedeem(context.Background(), 2, subscriptionCode(88, 7, 30))
	require.NoError(t, err)
	require.EqualValues(t, 0, pts, "订阅基数缺失 → 不返")
	require.Empty(t, prepo.calls, "base 缺失时不应入账")
}

func TestAccrueEarnForRedeem_PerUserRateOverride(t *testing.T) {
	t.Parallel()
	override := 50.0
	aff := &pointsEarnAffiliateRepo{
		summaries: inviteePair(2, 1, &override),
		subBase:   map[string]float64{"7:30": 100},
	}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(pointsEarnDefaults(), aff, prepo)

	// 邀请人专属 50% 覆盖全局 20% → floor(100 × 50% / 0.01) = 5000。
	pts, err := svc.AccrueEarnForRedeem(context.Background(), 2, subscriptionCode(77, 7, 30))
	require.NoError(t, err)
	require.EqualValues(t, 5000, pts)
}

func TestAccrueEarnForRedeem_NoInviter(t *testing.T) {
	t.Parallel()
	aff := &pointsEarnAffiliateRepo{summaries: map[int64]*AffiliateSummary{
		2: {UserID: 2}, // 无 InviterID
	}}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(pointsEarnDefaults(), aff, prepo)

	pts, err := svc.AccrueEarnForRedeem(context.Background(), 2, balanceCode(77, 100))
	require.NoError(t, err)
	require.EqualValues(t, 0, pts)
	require.Empty(t, prepo.calls)
}

func TestAccrueEarnForRedeem_SelfInvite(t *testing.T) {
	t.Parallel()
	self := int64(2)
	aff := &pointsEarnAffiliateRepo{summaries: map[int64]*AffiliateSummary{
		2: {UserID: 2, InviterID: &self}, // 邀请人就是自己
	}}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(pointsEarnDefaults(), aff, prepo)

	pts, err := svc.AccrueEarnForRedeem(context.Background(), 2, balanceCode(77, 100))
	require.NoError(t, err)
	require.EqualValues(t, 0, pts, "自邀请不返")
	require.Empty(t, prepo.calls)
}

func TestAccrueEarnForRedeem_ZeroRate(t *testing.T) {
	t.Parallel()
	settings := pointsEarnDefaults()
	settings[SettingKeyPointsCashbackRate] = "0"
	aff := &pointsEarnAffiliateRepo{summaries: inviteePair(2, 1, nil)}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(settings, aff, prepo)

	pts, err := svc.AccrueEarnForRedeem(context.Background(), 2, balanceCode(77, 100))
	require.NoError(t, err)
	require.EqualValues(t, 0, pts)
	require.Empty(t, prepo.calls)
}

func TestAccrueEarnForRedeem_Disabled(t *testing.T) {
	t.Parallel()
	settings := pointsEarnDefaults()
	settings[SettingKeyPointsEnabled] = "false"
	aff := &pointsEarnAffiliateRepo{summaries: inviteePair(2, 1, nil)}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(settings, aff, prepo)

	pts, err := svc.AccrueEarnForRedeem(context.Background(), 2, balanceCode(77, 100))
	require.NoError(t, err)
	require.EqualValues(t, 0, pts)
	require.Empty(t, prepo.calls)
	require.Zero(t, aff.ensureN, "停用时不应触碰身份图谱")
}

func TestAccrueEarnForRedeem_NilOrZeroCode(t *testing.T) {
	t.Parallel()
	aff := &pointsEarnAffiliateRepo{summaries: inviteePair(2, 1, nil)}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(pointsEarnDefaults(), aff, prepo)

	pts, err := svc.AccrueEarnForRedeem(context.Background(), 2, nil)
	require.NoError(t, err)
	require.EqualValues(t, 0, pts)

	pts, err = svc.AccrueEarnForRedeem(context.Background(), 2, balanceCode(0, 100))
	require.NoError(t, err)
	require.EqualValues(t, 0, pts, "无效兑换码 ID 不返")
	require.Empty(t, prepo.calls)
}

func TestAccrueEarnForRedeem_BalanceCodeZeroValue(t *testing.T) {
	t.Parallel()
	aff := &pointsEarnAffiliateRepo{summaries: inviteePair(2, 1, nil)}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(pointsEarnDefaults(), aff, prepo)

	pts, err := svc.AccrueEarnForRedeem(context.Background(), 2, balanceCode(77, 0))
	require.NoError(t, err)
	require.EqualValues(t, 0, pts)
	require.Empty(t, prepo.calls)
}

func TestAccrueEarnForRedeem_UnknownType(t *testing.T) {
	t.Parallel()
	aff := &pointsEarnAffiliateRepo{summaries: inviteePair(2, 1, nil)}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(pointsEarnDefaults(), aff, prepo)

	pts, err := svc.AccrueEarnForRedeem(context.Background(), 2, &RedeemCode{ID: 5, Type: RedeemTypeConcurrency, Value: 100})
	require.NoError(t, err)
	require.EqualValues(t, 0, pts, "并发码不返积分")
	require.Empty(t, prepo.calls)
}

func TestAccrueEarnForRedeem_Idempotent(t *testing.T) {
	t.Parallel()
	aff := &pointsEarnAffiliateRepo{
		summaries: inviteePair(2, 1, nil),
		subBase:   map[string]float64{"7:30": 100},
	}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(pointsEarnDefaults(), aff, prepo)
	code := subscriptionCode(77, 7, 30)

	pts1, err := svc.AccrueEarnForRedeem(context.Background(), 2, code)
	require.NoError(t, err)
	require.EqualValues(t, 2000, pts1)

	// 同一兑换码重放：repo 命中 partial-unique → applied=false → service 返回 0。
	pts2, err := svc.AccrueEarnForRedeem(context.Background(), 2, code)
	require.NoError(t, err)
	require.EqualValues(t, 0, pts2, "重放不重复入账")
	require.Len(t, prepo.calls, 2, "两次都到达 repo（由 DB 约束去重）")
}

func TestAccrueEarnForRedeem_RepoError(t *testing.T) {
	t.Parallel()
	aff := &pointsEarnAffiliateRepo{
		summaries: inviteePair(2, 1, nil),
		subBase:   map[string]float64{"7:30": 100},
	}
	prepo := &pointsEarnRepo{forceErr: errors.New("db down")}
	svc := newEarnPointsService(pointsEarnDefaults(), aff, prepo)

	pts, err := svc.AccrueEarnForRedeem(context.Background(), 2, subscriptionCode(77, 7, 30))
	require.Error(t, err)
	require.EqualValues(t, 0, pts)
}

// ============================ AccrueEarnForOrder ============================

// 法币订单返积分对「订单种类」无感：base 优先取实付 PayAmount，充值单与订阅单一视同仁。
// 这正是「套餐单不漏返」的逻辑保证——只要订阅购买生成了实付 PaymentOrder 即返。
func TestAccrueEarnForOrder_UsesPaidAmountRegardlessOfOrderKind(t *testing.T) {
	t.Parallel()
	aff := &pointsEarnAffiliateRepo{summaries: inviteePair(2, 1, nil)}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(pointsEarnDefaults(), aff, prepo)

	order := paymentOrder(2, 100) // 模拟订阅购买订单：UserID=2, Amount=100
	order.PayAmount = 45
	pts, err := svc.AccrueEarnForOrder(context.Background(), order)
	require.NoError(t, err)
	require.EqualValues(t, 900, pts)
	require.Len(t, prepo.calls, 1)
	require.EqualValues(t, order.ID, prepo.calls[0].SourceOrderID, "订单来源锚")
	require.EqualValues(t, 0, prepo.calls[0].SourceRedeemCodeID, "订单 earning 不写兑换码锚")
}

func TestAccrueEarnForOrder_FirstSuccessfulPaymentGetsDoublePoints(t *testing.T) {
	client := newPointsEarnEntClient(t)
	invitee := createPointsEarnUser(t, client, "first-pay-invitee@example.com")
	order := createPointsEarnOrder(t, client, invitee, OrderStatusCompleted, 100)

	aff := &pointsEarnAffiliateRepo{summaries: inviteePair(invitee.ID, 1001, nil)}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(pointsEarnDefaults(), aff, prepo)
	svc.entClient = client

	pts, err := svc.AccrueEarnForOrder(context.Background(), order)
	require.NoError(t, err)
	require.EqualValues(t, 4000, pts, "first successful fiat payment gets 2x points")
	require.Len(t, prepo.calls, 1)
	require.EqualValues(t, 4000, prepo.calls[0].Points)
}

func TestAccrueEarnForOrder_NonFirstSuccessfulPaymentUsesNormalPoints(t *testing.T) {
	client := newPointsEarnEntClient(t)
	invitee := createPointsEarnUser(t, client, "repeat-pay-invitee@example.com")
	_ = createPointsEarnOrder(t, client, invitee, OrderStatusCompleted, 50)
	order := createPointsEarnOrder(t, client, invitee, OrderStatusCompleted, 100)

	aff := &pointsEarnAffiliateRepo{summaries: inviteePair(invitee.ID, 1001, nil)}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(pointsEarnDefaults(), aff, prepo)
	svc.entClient = client

	pts, err := svc.AccrueEarnForOrder(context.Background(), order)
	require.NoError(t, err)
	require.EqualValues(t, 2000, pts)
	require.Len(t, prepo.calls, 1)
	require.EqualValues(t, 2000, prepo.calls[0].Points)
}

func TestAccrueEarnForOrder_ZeroAmount(t *testing.T) {
	t.Parallel()
	aff := &pointsEarnAffiliateRepo{summaries: inviteePair(2, 1, nil)}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(pointsEarnDefaults(), aff, prepo)

	pts, err := svc.AccrueEarnForOrder(context.Background(), paymentOrder(2, 0))
	require.NoError(t, err)
	require.EqualValues(t, 0, pts)
	require.Empty(t, prepo.calls)
}

func TestAccrueEarnForOrder_NoInviter(t *testing.T) {
	t.Parallel()
	aff := &pointsEarnAffiliateRepo{summaries: map[int64]*AffiliateSummary{2: {UserID: 2}}}
	prepo := &pointsEarnRepo{}
	svc := newEarnPointsService(pointsEarnDefaults(), aff, prepo)

	pts, err := svc.AccrueEarnForOrder(context.Background(), paymentOrder(2, 100))
	require.NoError(t, err)
	require.EqualValues(t, 0, pts)
	require.Empty(t, prepo.calls)
}
