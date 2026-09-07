//go:build integration

package repository

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *AccountRepoSuite) TestList_DefaultSortByNameAsc() {
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "z-account"})
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "a-account"})

	accounts, _, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10})
	s.Require().NoError(err)
	s.Require().Len(accounts, 2)
	s.Require().Equal("a-account", accounts[0].Name)
	s.Require().Equal("z-account", accounts[1].Name)
}

func (s *AccountRepoSuite) TestListWithFilters_SortByPriorityDesc() {
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "low-priority", Priority: 10})
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "high-priority", Priority: 90})

	accounts, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{
		Page:      1,
		PageSize:  10,
		SortBy:    "priority",
		SortOrder: "desc",
	}, "", "", "", "", 0, "")
	s.Require().NoError(err)
	s.Require().Len(accounts, 2)
	s.Require().Equal("high-priority", accounts[0].Name)
	s.Require().Equal("low-priority", accounts[1].Name)
}

func (s *AccountRepoSuite) TestListWithFilters_SortByUpstreamBillingRateWithNullsLast() {
	makeAccount := func(name, status string, rate any) {
		extra := map[string]any{}
		if rate != nil {
			extra[service.UpstreamBillingProbeExtraKey] = map[string]any{
				"status": status,
				"data": map[string]any{
					"effective_rate_multiplier": rate,
				},
			}
		}
		mustCreateAccount(s.T(), s.client, &service.Account{
			Name:     name,
			Platform: service.PlatformOpenAI,
			Type:     service.AccountTypeAPIKey,
			Extra:    extra,
		})
	}
	makeAccount("high-rate", service.UpstreamBillingProbeStatusOK, 0.8)
	makeAccount("low-rate", service.UpstreamBillingProbeStatusOK, 0.03)
	makeAccount("missing-rate", "", nil)
	makeAccount("unsupported-with-retained-rate", service.UpstreamBillingProbeStatusUnsupported, 0.01)

	for _, tc := range []struct {
		order string
		want  []string
	}{
		{order: "asc", want: []string{"low-rate", "high-rate", "missing-rate", "unsupported-with-retained-rate"}},
		{order: "desc", want: []string{"high-rate", "low-rate", "unsupported-with-retained-rate", "missing-rate"}},
	} {
		accounts, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{
			Page:      1,
			PageSize:  10,
			SortBy:    "upstream_billing_rate",
			SortOrder: tc.order,
		}, "", "", "", "", 0, "")
		s.Require().NoError(err)
		s.Require().Equal(tc.want, []string{accounts[0].Name, accounts[1].Name, accounts[2].Name, accounts[3].Name})
	}
}

// TestListWithFilters_SortByUpstreamBillingRateIsPeakWindowAndTimezoneSafe covers the
// dynamic (peak-window aware) branch of upstreamBillingRateSortExpression, which the
// nulls-last test above does not exercise (it only covers the legacy
// effective_rate_multiplier-only snapshot shape). It intentionally avoids any
// assertion that depends on the current wall-clock time being inside/outside a peak
// window: every non-trivial case here is deterministic regardless of when the test
// runs, so it stays reliable in CI while still proving the sort expression fails
// closed (NULL, sorted last) instead of erroring or misordering on malformed
// peak-window/timezone/billing-scope data.
func (s *AccountRepoSuite) TestListWithFilters_SortByUpstreamBillingRateIsPeakWindowAndTimezoneSafe() {
	makeDynamicAccount := func(name string, data map[string]any) {
		mustCreateAccount(s.T(), s.client, &service.Account{
			Name:     name,
			Platform: service.PlatformOpenAI,
			Type:     service.AccountTypeAPIKey,
			Extra: map[string]any{
				service.UpstreamBillingProbeExtraKey: map[string]any{
					"status": service.UpstreamBillingProbeStatusOK,
					"data":   data,
				},
			},
		})
	}

	// Deterministic: peak rate disabled resolves directly to the resolved rate,
	// independent of the current time.
	makeDynamicAccount("peak-disabled", map[string]any{
		"billing_scope":            "token",
		"resolved_rate_multiplier": 0.5,
		"peak_rate_enabled":        false,
	})
	// Everything below has a valid resolved_rate_multiplier but a broken piece of
	// the peak-window/timezone/billing-scope contract, so the sort expression must
	// null it out rather than crash or silently mis-sort it.
	makeDynamicAccount("invalid-timezone", map[string]any{
		"billing_scope":            "token",
		"resolved_rate_multiplier": 0.5,
		"peak_rate_enabled":        true,
		"peak_start":               "09:00",
		"peak_end":                 "18:00",
		"peak_rate_multiplier":     1.5,
		"timezone":                 "Not/ARealZone",
	})
	makeDynamicAccount("invalid-clock-format", map[string]any{
		"billing_scope":            "token",
		"resolved_rate_multiplier": 0.5,
		"peak_rate_enabled":        true,
		"peak_start":               "25:00",
		"peak_end":                 "18:00",
		"peak_rate_multiplier":     1.5,
		"timezone":                 "UTC",
	})
	makeDynamicAccount("invalid-window-order", map[string]any{
		"billing_scope":            "token",
		"resolved_rate_multiplier": 0.5,
		"peak_rate_enabled":        true,
		"peak_start":               "18:00",
		"peak_end":                 "09:00",
		"peak_rate_multiplier":     1.5,
		"timezone":                 "UTC",
	})
	makeDynamicAccount("negative-peak-multiplier", map[string]any{
		"billing_scope":            "token",
		"resolved_rate_multiplier": 0.5,
		"peak_rate_enabled":        true,
		"peak_start":               "09:00",
		"peak_end":                 "18:00",
		"peak_rate_multiplier":     -1,
		"timezone":                 "UTC",
	})
	makeDynamicAccount("non-token-billing-scope", map[string]any{
		"billing_scope":            "request",
		"resolved_rate_multiplier": 0.5,
		"peak_rate_enabled":        false,
	})

	accounts, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{
		Page:      1,
		PageSize:  10,
		SortBy:    "upstream_billing_rate",
		SortOrder: "asc",
	}, "", "", "", "", 0, "")
	s.Require().NoError(err)
	s.Require().Len(accounts, 6)
	s.Require().Equal("peak-disabled", accounts[0].Name, "the only account with a resolvable rate must sort first")

	sortedLast := make([]string, 0, 5)
	for _, account := range accounts[1:] {
		sortedLast = append(sortedLast, account.Name)
	}
	s.Require().ElementsMatch(
		[]string{
			"invalid-timezone",
			"invalid-clock-format",
			"invalid-window-order",
			"negative-peak-multiplier",
			"non-token-billing-scope",
		},
		sortedLast,
		"malformed peak-window/timezone/billing-scope data must sort last (NULL) rather than erroring",
	)
}
