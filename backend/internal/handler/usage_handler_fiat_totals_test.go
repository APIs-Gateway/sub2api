package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 这组测试盯住「用户端用量统计的人民币合计」在 handler 层的装配：
// 筛选条件是否与配对的额度统计一致、分桶结果是否按维度 Key 填回正确的行、
// 以及倍率为 1 / 分桶失败时是否老老实实不填法币字段（前端据此回落到额度展示）。
// 钱包倍率统一用 13：1 元 = 13 额度，数字好心算。

type fiatTotalsUsageRepoStub struct {
	service.UsageLogRepository

	record     *service.UsageLog
	userStats  *usagestats.UsageStats
	keyStats   *usagestats.UsageStats
	dashboard  *usagestats.UserDashboardStats
	trend      []usagestats.TrendDataPoint
	modelStats []usagestats.ModelStat
	batchStats map[int64]*usagestats.BatchAPIKeyUsageStats
	// bucketsFor 按调用顺序返回分桶；nil 表示仓储「有能力但没数据」。
	bucketsFor func(filter usagestats.CreditCostBucketFilter) ([]usagestats.CreditCostBucket, error)

	bucketFilters []usagestats.CreditCostBucketFilter
}

func (s *fiatTotalsUsageRepoStub) GetByID(ctx context.Context, id int64) (*service.UsageLog, error) {
	return s.record, nil
}

func (s *fiatTotalsUsageRepoStub) GetUserStatsAggregated(ctx context.Context, userID int64, startTime, endTime time.Time) (*usagestats.UsageStats, error) {
	return s.userStats, nil
}

func (s *fiatTotalsUsageRepoStub) GetAPIKeyStatsAggregated(ctx context.Context, apiKeyID int64, startTime, endTime time.Time) (*usagestats.UsageStats, error) {
	return s.keyStats, nil
}

func (s *fiatTotalsUsageRepoStub) GetUserDashboardStats(ctx context.Context, userID int64) (*usagestats.UserDashboardStats, error) {
	return s.dashboard, nil
}

func (s *fiatTotalsUsageRepoStub) GetUserUsageTrendByUserID(ctx context.Context, userID int64, startTime, endTime time.Time, granularity string) ([]usagestats.TrendDataPoint, error) {
	return s.trend, nil
}

func (s *fiatTotalsUsageRepoStub) GetUserModelStats(ctx context.Context, userID int64, startTime, endTime time.Time) ([]usagestats.ModelStat, error) {
	return s.modelStats, nil
}

func (s *fiatTotalsUsageRepoStub) GetBatchAPIKeyUsageStats(ctx context.Context, apiKeyIDs []int64, startTime, endTime time.Time) (map[int64]*usagestats.BatchAPIKeyUsageStats, error) {
	return s.batchStats, nil
}

func (s *fiatTotalsUsageRepoStub) GetUsageTrendWithFilters(
	ctx context.Context,
	startTime, endTime time.Time,
	granularity string,
	userID, apiKeyID, accountID, groupID int64,
	model string,
	requestType *int16,
	stream *bool,
	billingType *int8,
) ([]usagestats.TrendDataPoint, error) {
	return s.trend, nil
}

func (s *fiatTotalsUsageRepoStub) GetCreditCostBuckets(ctx context.Context, filter usagestats.CreditCostBucketFilter) ([]usagestats.CreditCostBucket, error) {
	s.bucketFilters = append(s.bucketFilters, filter)
	if s.bucketsFor == nil {
		return nil, nil
	}
	return s.bucketsFor(filter)
}

// 只带 GetByID / VerifyOwnership 的 Key 仓储桩。
type fiatTotalsAPIKeyRepoStub struct {
	service.APIKeyRepository
	owner map[int64]int64 // keyID -> userID
}

func (s *fiatTotalsAPIKeyRepoStub) GetByID(ctx context.Context, id int64) (*service.APIKey, error) {
	uid, ok := s.owner[id]
	if !ok {
		return nil, service.ErrAPIKeyNotFound
	}
	return &service.APIKey{ID: id, UserID: uid}, nil
}

func (s *fiatTotalsAPIKeyRepoStub) VerifyOwnership(ctx context.Context, userID int64, apiKeyIDs []int64) ([]int64, error) {
	valid := make([]int64, 0, len(apiKeyIDs))
	for _, id := range apiKeyIDs {
		if s.owner[id] == userID {
			valid = append(valid, id)
		}
	}
	return valid, nil
}

type fiatTotalsEnv struct {
	repo   *fiatTotalsUsageRepoStub
	router *gin.Engine
}

func newFiatTotalsEnv(t *testing.T, multiplier string, repo *fiatTotalsUsageRepoStub) *fiatTotalsEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	usageSvc := service.NewUsageService(repo, nil, nil, nil)
	keySvc := service.NewAPIKeyService(&fiatTotalsAPIKeyRepoStub{owner: map[int64]int64{11: 7, 12: 7, 99: 8}}, nil, nil, nil, nil, nil, nil)
	h := NewUsageHandler(usageSvc, keySvc, nil, newCreditFiatSettingService(multiplier))

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7})
		c.Next()
	})
	router.GET("/usage/:id", h.GetByID)
	router.GET("/stats", h.Stats)
	router.GET("/dashboard/stats", h.DashboardStats)
	router.GET("/dashboard/trend", h.DashboardTrend)
	router.GET("/dashboard/models", h.DashboardModels)
	router.POST("/dashboard/api-keys-usage", h.DashboardAPIKeysUsage)
	router.GET("/keys/:id/daily", h.GetMyAPIKeyDailyUsage)
	return &fiatTotalsEnv{repo: repo, router: router}
}

func (e *fiatTotalsEnv) do(t *testing.T, method, target, body string) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return rec.Code, out
}

func dataOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	return asObj(t, body["data"])
}

func walletBucket(key string, cost, since float64) usagestats.CreditCostBucket {
	return usagestats.CreditCostBucket{Key: key, BillingType: service.BillingTypeBalance, ActualCost: cost, ActualCostSince: since}
}

// 单条记录详情要和列表同口径：带上法币单价与法币金额。
func TestUsageHandler_GetByID_AttachesFiatFields(t *testing.T) {
	repo := &fiatTotalsUsageRepoStub{record: &service.UsageLog{ID: 5, UserID: 7, BillingType: service.BillingTypeBalance, ActualCost: 6.5}}
	env := newFiatTotalsEnv(t, "13", repo)

	code, body := env.do(t, http.MethodGet, "/usage/5", "")

	require.Equal(t, http.StatusOK, code)
	data := dataOf(t, body)
	require.InDelta(t, 0.5, data["fiat_cost"], 1e-9)
	require.InDelta(t, 1.0/13.0, data["fiat_per_credit"], 1e-9)
}

// Stats：不带 api_key_id 时筛选只含用户和时间窗，合计取无维度的 "" 桶，
// 订阅与钱包混合的多桶要相加：(13 + 26) / 13 = 3。
func TestUsageHandler_Stats_FillsFiatTotalAcrossBuckets(t *testing.T) {
	repo := &fiatTotalsUsageRepoStub{
		userStats: &usagestats.UsageStats{TotalActualCost: 39},
		bucketsFor: func(usagestats.CreditCostBucketFilter) ([]usagestats.CreditCostBucket, error) {
			return []usagestats.CreditCostBucket{
				walletBucket("", 13, 0),
				{Key: "", BillingType: service.BillingTypeSubscription, SubscriptionID: 202, ActualCost: 26},
			}, nil
		},
	}
	env := newFiatTotalsEnv(t, "13", repo)

	code, body := env.do(t, http.MethodGet, "/stats?start_date=2026-09-01&end_date=2026-09-30", "")

	require.Equal(t, http.StatusOK, code)
	require.InDelta(t, 3.0, dataOf(t, body)["total_actual_cost_fiat"], 1e-9)
	require.Len(t, repo.bucketFilters, 1)
	f := repo.bucketFilters[0]
	require.EqualValues(t, 7, f.UserID)
	require.Empty(t, f.APIKeyIDs)
	require.Equal(t, usagestats.CreditBucketNone, f.Dimension)
	// 上边界是结束日的次日 00:00，与 SQL 的 created_at < end 对齐。
	require.Equal(t, 30*24*time.Hour, f.EndTime.Sub(f.StartTime))
}

// Stats 带 api_key_id：筛选必须带上该 Key，否则法币合计会是整个账号的而不是这把 Key 的。
func TestUsageHandler_Stats_ByAPIKeyScopesBucketFilter(t *testing.T) {
	repo := &fiatTotalsUsageRepoStub{
		keyStats: &usagestats.UsageStats{TotalActualCost: 13},
		bucketsFor: func(usagestats.CreditCostBucketFilter) ([]usagestats.CreditCostBucket, error) {
			return []usagestats.CreditCostBucket{walletBucket("", 13, 0)}, nil
		},
	}
	env := newFiatTotalsEnv(t, "13", repo)

	code, body := env.do(t, http.MethodGet, "/stats?api_key_id=11&period=week", "")

	require.Equal(t, http.StatusOK, code)
	require.InDelta(t, 1.0, dataOf(t, body)["total_actual_cost_fiat"], 1e-9)
	require.Len(t, repo.bucketFilters, 1)
	require.Equal(t, []int64{11}, repo.bucketFilters[0].APIKeyIDs)
}

// 倍率为 1（free 站）：不跑聚合、不填法币字段，前端回落到额度展示。
func TestUsageHandler_Stats_MultiplierOneOmitsFiat(t *testing.T) {
	repo := &fiatTotalsUsageRepoStub{userStats: &usagestats.UsageStats{TotalActualCost: 39}}
	env := newFiatTotalsEnv(t, "1", repo)

	code, body := env.do(t, http.MethodGet, "/stats", "")

	require.Equal(t, http.StatusOK, code)
	require.NotContains(t, dataOf(t, body), "total_actual_cost_fiat")
	require.Empty(t, repo.bucketFilters)
}

// 分桶查询失败不能拖垮统计接口：额度合计照常返回，法币字段缺省。
func TestUsageHandler_Stats_BucketErrorKeepsCreditStats(t *testing.T) {
	repo := &fiatTotalsUsageRepoStub{
		userStats: &usagestats.UsageStats{TotalActualCost: 39},
		bucketsFor: func(usagestats.CreditCostBucketFilter) ([]usagestats.CreditCostBucket, error) {
			return nil, errors.New("db down")
		},
	}
	env := newFiatTotalsEnv(t, "13", repo)

	code, body := env.do(t, http.MethodGet, "/stats", "")

	require.Equal(t, http.StatusOK, code)
	data := dataOf(t, body)
	require.InDelta(t, 39.0, data["total_actual_cost"], 1e-9)
	require.NotContains(t, data, "total_actual_cost_fiat")
}

// 空范围（0 个桶）：法币合计是 0 而不是报错，omitempty 下字段缺省。
func TestUsageHandler_Stats_NoBucketsYieldsZeroFiat(t *testing.T) {
	repo := &fiatTotalsUsageRepoStub{userStats: &usagestats.UsageStats{}}
	env := newFiatTotalsEnv(t, "13", repo)

	code, body := env.do(t, http.MethodGet, "/stats", "")

	require.Equal(t, http.StatusOK, code)
	require.NotContains(t, dataOf(t, body), "total_actual_cost_fiat")
}

// 仪表盘：总计/今日取 "" 桶，by_platform 再按平台维度取一次，按平台名对应回行。
func TestUsageHandler_DashboardStats_FillsTotalsTodayAndPlatforms(t *testing.T) {
	repo := &fiatTotalsUsageRepoStub{
		dashboard: &usagestats.UserDashboardStats{
			ByPlatform: []usagestats.PlatformDashboardStats{{Platform: "openai"}, {Platform: "anthropic"}, {Platform: "gemini"}},
		},
		bucketsFor: func(f usagestats.CreditCostBucketFilter) ([]usagestats.CreditCostBucket, error) {
			if f.Dimension == usagestats.CreditBucketPlatform {
				return []usagestats.CreditCostBucket{
					walletBucket("openai", 26, 13),
					walletBucket("anthropic", 13, 0),
				}, nil
			}
			return []usagestats.CreditCostBucket{walletBucket("", 39, 13)}, nil
		},
	}
	env := newFiatTotalsEnv(t, "13", repo)

	code, body := env.do(t, http.MethodGet, "/dashboard/stats", "")

	require.Equal(t, http.StatusOK, code)
	data := dataOf(t, body)
	require.InDelta(t, 3.0, data["total_actual_cost_fiat"], 1e-9)
	require.InDelta(t, 1.0, data["today_actual_cost_fiat"], 1e-9)

	platforms := asList(t, data["by_platform"])
	require.Len(t, platforms, 3)
	openai := asObj(t, platforms[0])
	require.InDelta(t, 2.0, openai["total_actual_cost_fiat"], 1e-9)
	require.InDelta(t, 1.0, openai["today_actual_cost_fiat"], 1e-9)
	anthropic := asObj(t, platforms[1])
	require.InDelta(t, 1.0, anthropic["total_actual_cost_fiat"], 1e-9)
	require.NotContains(t, anthropic, "today_actual_cost_fiat")
	// 没有分桶的平台保持 0（字段缺省），不会串用别的平台的值。
	require.NotContains(t, asObj(t, platforms[2]), "total_actual_cost_fiat")

	require.Len(t, repo.bucketFilters, 2)
	require.False(t, repo.bucketFilters[0].SplitAt.IsZero())
	require.True(t, repo.bucketFilters[0].StartTime.IsZero(), "总计不设时间下界")
	require.Equal(t, usagestats.CreditBucketPlatform, repo.bucketFilters[1].Dimension)
}

// 没有任何平台明细时只查一次聚合。
func TestUsageHandler_DashboardStats_NoPlatformsSkipsSecondQuery(t *testing.T) {
	repo := &fiatTotalsUsageRepoStub{
		dashboard: &usagestats.UserDashboardStats{},
		bucketsFor: func(usagestats.CreditCostBucketFilter) ([]usagestats.CreditCostBucket, error) {
			return []usagestats.CreditCostBucket{walletBucket("", 13, 13)}, nil
		},
	}
	env := newFiatTotalsEnv(t, "13", repo)

	code, body := env.do(t, http.MethodGet, "/dashboard/stats", "")

	require.Equal(t, http.StatusOK, code)
	require.InDelta(t, 1.0, dataOf(t, body)["total_actual_cost_fiat"], 1e-9)
	require.Len(t, repo.bucketFilters, 1)
}

// 平台维度的查询失败：总计/今日已填的法币保留，平台行不填。
func TestUsageHandler_DashboardStats_PlatformQueryFailureKeepsTotals(t *testing.T) {
	repo := &fiatTotalsUsageRepoStub{
		dashboard: &usagestats.UserDashboardStats{ByPlatform: []usagestats.PlatformDashboardStats{{Platform: "openai"}}},
		bucketsFor: func(f usagestats.CreditCostBucketFilter) ([]usagestats.CreditCostBucket, error) {
			if f.Dimension == usagestats.CreditBucketPlatform {
				return nil, errors.New("boom")
			}
			return []usagestats.CreditCostBucket{walletBucket("", 13, 0)}, nil
		},
	}
	env := newFiatTotalsEnv(t, "13", repo)

	code, body := env.do(t, http.MethodGet, "/dashboard/stats", "")

	require.Equal(t, http.StatusOK, code)
	data := dataOf(t, body)
	require.InDelta(t, 1.0, data["total_actual_cost_fiat"], 1e-9)
	require.NotContains(t, asObj(t, asList(t, data["by_platform"])[0]), "total_actual_cost_fiat")
}

// 倍率为 1 时仪表盘不填任何法币字段。
func TestUsageHandler_DashboardStats_MultiplierOneOmitsFiat(t *testing.T) {
	repo := &fiatTotalsUsageRepoStub{
		dashboard: &usagestats.UserDashboardStats{ByPlatform: []usagestats.PlatformDashboardStats{{Platform: "openai"}}},
	}
	env := newFiatTotalsEnv(t, "1", repo)

	code, body := env.do(t, http.MethodGet, "/dashboard/stats", "")

	require.Equal(t, http.StatusOK, code)
	require.NotContains(t, dataOf(t, body), "total_actual_cost_fiat")
	require.Empty(t, repo.bucketFilters)
}

// 趋势：按日期 Key 对应回每个点；没有对应桶的日期为 0。
func TestUsageHandler_DashboardTrend_FillsFiatPerDate(t *testing.T) {
	repo := &fiatTotalsUsageRepoStub{
		trend: []usagestats.TrendDataPoint{{Date: "2026-09-01"}, {Date: "2026-09-02"}, {Date: "2026-09-03"}},
		bucketsFor: func(usagestats.CreditCostBucketFilter) ([]usagestats.CreditCostBucket, error) {
			return []usagestats.CreditCostBucket{
				walletBucket("2026-09-01", 13, 0),
				walletBucket("2026-09-02", 6.5, 0),
				{Key: "2026-09-02", BillingType: service.BillingTypeSubscription, SubscriptionID: 5, ActualCost: 6.5},
			}, nil
		},
	}
	env := newFiatTotalsEnv(t, "13", repo)

	code, body := env.do(t, http.MethodGet, "/dashboard/trend?granularity=day&start_date=2026-09-01&end_date=2026-09-03", "")

	require.Equal(t, http.StatusOK, code)
	points := asList(t, dataOf(t, body)["trend"])
	require.Len(t, points, 3)
	require.InDelta(t, 1.0, asObj(t, points[0])["actual_cost_fiat"], 1e-9)
	require.InDelta(t, 1.0, asObj(t, points[1])["actual_cost_fiat"], 1e-9)
	require.NotContains(t, asObj(t, points[2]), "actual_cost_fiat")

	require.Len(t, repo.bucketFilters, 1)
	require.Equal(t, usagestats.CreditBucketDate, repo.bucketFilters[0].Dimension)
	require.Equal(t, "day", repo.bucketFilters[0].Granularity)
}

// 趋势为空时不白跑聚合查询。
func TestUsageHandler_DashboardTrend_EmptySkipsFiatQuery(t *testing.T) {
	repo := &fiatTotalsUsageRepoStub{}
	env := newFiatTotalsEnv(t, "13", repo)

	code, _ := env.do(t, http.MethodGet, "/dashboard/trend", "")

	require.Equal(t, http.StatusOK, code)
	require.Empty(t, repo.bucketFilters)
}

// 模型：必须用 requested 口径分桶，和模型统计列表的口径一致。
func TestUsageHandler_DashboardModels_FillsFiatPerModel(t *testing.T) {
	repo := &fiatTotalsUsageRepoStub{
		modelStats: []usagestats.ModelStat{{Model: "gpt-5"}, {Model: "claude"}},
		bucketsFor: func(usagestats.CreditCostBucketFilter) ([]usagestats.CreditCostBucket, error) {
			return []usagestats.CreditCostBucket{walletBucket("gpt-5", 26, 0)}, nil
		},
	}
	env := newFiatTotalsEnv(t, "13", repo)

	code, body := env.do(t, http.MethodGet, "/dashboard/models", "")

	require.Equal(t, http.StatusOK, code)
	models := asList(t, dataOf(t, body)["models"])
	require.InDelta(t, 2.0, asObj(t, models[0])["actual_cost_fiat"], 1e-9)
	require.NotContains(t, asObj(t, models[1]), "actual_cost_fiat")

	require.Len(t, repo.bucketFilters, 1)
	require.Equal(t, usagestats.CreditBucketModel, repo.bucketFilters[0].Dimension)
	require.Equal(t, usagestats.ModelSourceRequested, repo.bucketFilters[0].ModelSource)
}

func TestUsageHandler_DashboardModels_EmptySkipsFiatQuery(t *testing.T) {
	repo := &fiatTotalsUsageRepoStub{}
	env := newFiatTotalsEnv(t, "13", repo)

	code, _ := env.do(t, http.MethodGet, "/dashboard/models", "")

	require.Equal(t, http.StatusOK, code)
	require.Empty(t, repo.bucketFilters)
}

// 多把 Key 的批量用量：只统计属于自己的 Key，总计与今日按 Key ID 对应回去。
func TestUsageHandler_DashboardAPIKeysUsage_FillsFiatPerKey(t *testing.T) {
	repo := &fiatTotalsUsageRepoStub{
		batchStats: map[int64]*usagestats.BatchAPIKeyUsageStats{
			11: {APIKeyID: 11},
			12: {APIKeyID: 12},
		},
		bucketsFor: func(usagestats.CreditCostBucketFilter) ([]usagestats.CreditCostBucket, error) {
			return []usagestats.CreditCostBucket{
				walletBucket("11", 26, 13),
				walletBucket("12", 13, 0),
			}, nil
		},
	}
	env := newFiatTotalsEnv(t, "13", repo)

	// 99 属于别的用户，必须被过滤掉。
	code, body := env.do(t, http.MethodPost, "/dashboard/api-keys-usage", `{"api_key_ids":[11,12,99]}`)

	require.Equal(t, http.StatusOK, code)
	stats := asObj(t, dataOf(t, body)["stats"])
	k11 := asObj(t, stats["11"])
	require.InDelta(t, 2.0, k11["total_actual_cost_fiat"], 1e-9)
	require.InDelta(t, 1.0, k11["today_actual_cost_fiat"], 1e-9)
	k12 := asObj(t, stats["12"])
	require.InDelta(t, 1.0, k12["total_actual_cost_fiat"], 1e-9)
	require.NotContains(t, k12, "today_actual_cost_fiat")

	require.Len(t, repo.bucketFilters, 1)
	f := repo.bucketFilters[0]
	require.Equal(t, []int64{11, 12}, f.APIKeyIDs)
	require.Equal(t, usagestats.CreditBucketAPIKey, f.Dimension)
	require.False(t, f.StartTime.IsZero())
	require.False(t, f.EndTime.IsZero())
	require.False(t, f.SplitAt.IsZero())
}

// 单把 Key 的按日用量：筛选限定这把 Key、按 day 分桶，日期对应回每个点。
func TestUsageHandler_GetMyAPIKeyDailyUsage_FillsFiatPerDay(t *testing.T) {
	repo := &fiatTotalsUsageRepoStub{
		trend: []usagestats.TrendDataPoint{{Date: "2026-09-01"}, {Date: "2026-09-02"}},
		bucketsFor: func(usagestats.CreditCostBucketFilter) ([]usagestats.CreditCostBucket, error) {
			return []usagestats.CreditCostBucket{walletBucket("2026-09-02", 39, 0)}, nil
		},
	}
	env := newFiatTotalsEnv(t, "13", repo)

	code, body := env.do(t, http.MethodGet, "/keys/11/daily?days=2", "")

	require.Equal(t, http.StatusOK, code)
	items := asList(t, dataOf(t, body)["items"])
	require.Len(t, items, 2)
	require.NotContains(t, asObj(t, items[0]), "actual_cost_fiat")
	require.InDelta(t, 3.0, asObj(t, items[1])["actual_cost_fiat"], 1e-9)

	require.Len(t, repo.bucketFilters, 1)
	f := repo.bucketFilters[0]
	require.Equal(t, []int64{11}, f.APIKeyIDs)
	require.Equal(t, usagestats.CreditBucketDate, f.Dimension)
	require.Equal(t, "day", f.Granularity)
}

// 别人的 Key：403，且不能触发任何聚合查询。
func TestUsageHandler_Stats_ForeignAPIKeyForbidden(t *testing.T) {
	repo := &fiatTotalsUsageRepoStub{}
	env := newFiatTotalsEnv(t, "13", repo)

	code, _ := env.do(t, http.MethodGet, "/stats?api_key_id=99", "")

	require.Equal(t, http.StatusForbidden, code)
	require.Empty(t, repo.bucketFilters)
}

// 直接测 usageService 缺失的兜底：creditFiatTotals 返回 ok=false。
func TestUsageHandler_CreditFiatTotals_NilUsageService(t *testing.T) {
	h := NewUsageHandler(nil, nil, nil, newCreditFiatSettingService("13"))

	totals, ok := h.creditFiatTotals(newCreditFiatContext(), usagestats.CreditCostBucketFilter{UserID: 7})

	require.False(t, ok)
	require.Nil(t, totals)
}

// fillDashboardStatsFiat 对 nil 统计安全。
func TestUsageHandler_FillDashboardStatsFiat_NilStats(t *testing.T) {
	h := NewUsageHandler(nil, nil, nil, nil)

	require.NotPanics(t, func() { h.fillDashboardStatsFiat(newCreditFiatContext(), 7, nil) })
}

func asObj(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	require.True(t, ok, "expected JSON object, got %T", v)
	return m
}

func asList(t *testing.T, v any) []any {
	t.Helper()
	l, ok := v.([]any)
	require.True(t, ok, "expected JSON array, got %T", v)
	return l
}
