package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 成本核算规则写入器（sqlmock）：每条语句的顺序、历史行的内容，以及每一步失败时都原样返回错误。
// 真实 PostgreSQL 的行为见 TestCostRuleWriter_Integration。

type pwcStep struct {
	name   string
	expect func(mock sqlmock.Sqlmock, fail error)
}

func pwcQuery(name, pattern string, rows func() *sqlmock.Rows) pwcStep {
	return pwcStep{name, func(mock sqlmock.Sqlmock, fail error) {
		q := mock.ExpectQuery(pattern)
		if fail != nil {
			q.WillReturnError(fail)
			return
		}
		q.WillReturnRows(rows())
	}}
}

func pwcExec(name, pattern string) pwcStep {
	return pwcStep{name, func(mock sqlmock.Sqlmock, fail error) {
		e := mock.ExpectExec(pattern)
		if fail != nil {
			e.WillReturnError(fail)
			return
		}
		e.WillReturnResult(sqlmock.NewResult(0, 1))
	}}
}

func pwcState() pwcStep {
	return pwcStep{"lock group", func(mock sqlmock.Sqlmock, fail error) {
		if fail != nil {
			mock.ExpectQuery(`FROM group_model_config c`).WillReturnError(fail)
			return
		}
		pwxExpectStates(mock, true, pwxRow{int64(1), "v2", int64(3)})
	}}
}

func pwcEditable() pwcStep {
	return pwcQuery("editable", `SELECT source FROM cost_accounting_rules`, func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"source"}).AddRow("manual")
	})
}

func pwcLoadState(name string) pwcStep {
	return pwcQuery(name, `SELECT jsonb_build_object`, func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"state"}).AddRow(`{"name":"r"}`)
	})
}

func pwcBump() pwcStep {
	return pwcQuery("bump", `UPDATE group_model_config SET revision`, func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"revision"}).AddRow(int64(4))
	})
}

func pwcSpec() service.CostRuleSpec {
	one := 1e-6
	return service.CostRuleSpec{
		Name: "r", GroupIDs: []int64{1}, AccountIDs: []int64{2}, Enabled: true,
		Prices: []service.MatrixCostRulePrice{{Platform: "openai", Models: []string{"m"}, Price: service.MatrixCustomPrice{InputPrice: &one}}},
	}
}

var pwcOps = map[string]struct {
	steps []pwcStep
	run   func(tx service.MatrixTx) (*service.CostRuleWriteResult, error)
}{
	"create": {
		steps: []pwcStep{
			pwcState(),
			pwcQuery("insert rule", `INSERT INTO cost_accounting_rules`, func() *sqlmock.Rows {
				return sqlmock.NewRows([]string{"id"}).AddRow(int64(11))
			}),
			pwcExec("insert prices", `INSERT INTO cost_accounting_rule_prices`),
			pwcBump(),
			pwcLoadState("load after"),
			pwcExec("history", `INSERT INTO cost_accounting_rule_history`),
		},
		run: func(tx service.MatrixTx) (*service.CostRuleWriteResult, error) {
			return NewPricingCostRuleWriter().CreateTx(context.Background(), tx, 7, 1, 3, pwcSpec())
		},
	},
	"update": {
		steps: []pwcStep{
			pwcState(), pwcEditable(), pwcLoadState("load before"),
			pwcExec("update rule", `UPDATE cost_accounting_rules`),
			pwcExec("delete prices", `DELETE FROM cost_accounting_rule_prices`),
			pwcExec("insert prices", `INSERT INTO cost_accounting_rule_prices`),
			pwcBump(), pwcLoadState("load after"),
			pwcExec("history", `INSERT INTO cost_accounting_rule_history`),
		},
		run: func(tx service.MatrixTx) (*service.CostRuleWriteResult, error) {
			return NewPricingCostRuleWriter().UpdateTx(context.Background(), tx, 7, 1, 3, 11, pwcSpec())
		},
	},
	"delete": {
		steps: []pwcStep{
			pwcState(), pwcEditable(), pwcLoadState("load before"),
			pwcExec("delete rule", `DELETE FROM cost_accounting_rules`),
			pwcBump(),
			pwcExec("history", `INSERT INTO cost_accounting_rule_history`),
		},
		run: func(tx service.MatrixTx) (*service.CostRuleWriteResult, error) {
			return NewPricingCostRuleWriter().DeleteTx(context.Background(), tx, 7, 1, 3, 11)
		},
	},
}

func TestCostRuleWriter_HistoryAndErrorBranches(t *testing.T) {
	boom := errors.New("boom")
	for name, op := range pwcOps {
		// 全部成功：历史行是最后一条语句。
		tx, mock := pwxTx(t)
		for _, s := range op.steps {
			s.expect(mock, nil)
		}
		res, err := op.run(tx)
		require.NoError(t, err, name)
		require.Equal(t, int64(4), res.Revision, name)
		require.NoError(t, mock.ExpectationsWereMet(), name)

		// 每一步失败：错误原样返回，后面的语句不再执行。
		for i := range op.steps {
			tx, mock := pwxTx(t)
			for j := 0; j <= i; j++ {
				var fail error
				if j == i {
					fail = boom
				}
				op.steps[j].expect(mock, fail)
			}
			_, err := op.run(tx)
			require.ErrorIs(t, err, boom, fmt.Sprintf("%s step %d (%s)", name, i, op.steps[i].name))
			require.NoError(t, mock.ExpectationsWereMet(), name)
		}
	}
}

func TestCostRuleWriter_HistoryRowContent(t *testing.T) {
	// 创建：改前为 NULL，改后是读出来的完整内容；更新带两份；删除改后为 NULL。
	cases := map[string]struct {
		action        string
		before, after any
	}{
		"create": {"create", nil, `{"name":"r"}`},
		"update": {"update", `{"name":"r"}`, `{"name":"r"}`},
		"delete": {"delete", `{"name":"r"}`, nil},
	}
	for name, c := range cases {
		op := pwcOps[name]
		tx, mock := pwxTx(t)
		for _, s := range op.steps[:len(op.steps)-1] {
			s.expect(mock, nil)
		}
		mock.ExpectExec(`INSERT INTO cost_accounting_rule_history`).
			WithArgs(sqlmock.AnyArg(), int64(1), c.action, int64(7), int64(4), c.before, c.after).
			WillReturnResult(sqlmock.NewResult(0, 1))
		_, err := op.run(tx)
		require.NoError(t, err, name)
		require.NoError(t, mock.ExpectationsWereMet(), name)
	}
}

func TestLoadCostRuleState_NoRowAndScanError(t *testing.T) {
	// 规则不存在：返回 nil，不是错误。
	tx, mock := pwxTx(t)
	mock.ExpectQuery(`SELECT jsonb_build_object`).WillReturnRows(sqlmock.NewRows([]string{"state"}))
	got, err := loadCostRuleState(context.Background(), tx, 5)
	require.NoError(t, err)
	require.Nil(t, got)

	// 读到 NULL：扫描失败。
	tx, mock = pwxTx(t)
	mock.ExpectQuery(`SELECT jsonb_build_object`).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(nil))
	_, err = loadCostRuleState(context.Background(), tx, 5)
	require.Error(t, err)

	// 迭代出错。
	tx, mock = pwxTx(t)
	mock.ExpectQuery(`SELECT jsonb_build_object`).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(`{}`).RowError(0, errors.New("row")))
	_, err = loadCostRuleState(context.Background(), tx, 5)
	require.Error(t, err)
}
