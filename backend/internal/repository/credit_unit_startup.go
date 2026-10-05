package repository

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// initCreditUnitAtStartup 在启动时把 settings 表里的记账单位读一次，固定为本进程的单位。
//
// 读的是 CREDIT_CURRENCY / LEGACY_CREDIT_DIVISOR / CNY_CUTOVER_AT（以及启动检查用的充值倍率），
// 之后进程内不再重读：运行中改 settings 表不生效，换单位必须重启全部实例。
// 这样同一个进程里不会同时出现两种单位，也不会有「请求处理到一半单位变了」的窗口。
//
// 失败（读库出错、配置值非法、CNY 模式下充值倍率不是 1）一律返回错误，调用方拒绝启动：
// 记账单位判断错了会让每一笔扣费和充值错 13 倍，宁可起不来，也不能静默按默认值跑。
func initCreditUnitAtStartup(ctx context.Context, client *ent.Client, cfg *config.Config) error {
	if client == nil {
		return fmt.Errorf("nil ent client")
	}
	if cfg == nil {
		return fmt.Errorf("nil config")
	}
	vals, err := NewSettingRepository(client).GetMultiple(ctx, service.CreditUnitStartupSettingKeys())
	if err != nil {
		return fmt.Errorf("read credit unit settings: %w", err)
	}
	return applyCreditUnitStartup(vals, cfg.Default.RateMultiplier)
}

// applyCreditUnitStartup 校验并固定进程的记账单位，把告警写进日志。拆出来是为了不用数据库就能测。
func applyCreditUnitStartup(vals map[string]string, defaultRateMultiplier float64) error {
	unit, warnings, err := service.ResolveCreditUnitAtStartup(vals, defaultRateMultiplier)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		slog.Warn("credit unit startup check: " + w)
	}
	if err := service.InitCreditUnit(unit); err != nil {
		return err
	}
	slog.Info("credit unit fixed for this process; changing the settings requires a restart", "credit_unit", unit.String())
	return nil
}
