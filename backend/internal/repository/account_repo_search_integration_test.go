//go:build integration

package repository

import (
	"sort"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *AccountRepoSuite) setAccountNotes(accountID int64, notes string) {
	_, err := s.client.Account.UpdateOneID(accountID).SetNotes(notes).Save(s.ctx)
	s.Require().NoError(err)
}

// searchAccountIDs 用列表接口搜索，返回命中的账号 ID（升序）。
func (s *AccountRepoSuite) searchAccountIDs(search string) []int64 {
	accounts, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 100}, "", "", "", search, 0, "")
	s.Require().NoError(err)
	ids := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (s *AccountRepoSuite) TestListWithFilters_SearchMatchesNameNotesBaseURLAndID() {
	byName := mustCreateAccount(s.T(), s.client, &service.Account{Name: "srch-alpha-name"})
	byNotes := mustCreateAccount(s.T(), s.client, &service.Account{Name: "srch-notes-holder"})
	s.setAccountNotes(byNotes.ID, "Contact: Zeta-Team")
	byBaseURL := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "srch-url-holder",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://API.Relay-Example.com/v1", "api_key": "sk-secret-value"},
	})
	byID := mustCreateAccount(s.T(), s.client, &service.Account{Name: "srch-id-holder"})
	mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "srch-unrelated",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://other.test"},
	})

	s.Require().Equal([]int64{byName.ID}, s.searchAccountIDs("alpha-name"), "name")
	s.Require().Equal([]int64{byNotes.ID}, s.searchAccountIDs("zeta-team"), "notes, case-insensitive")
	s.Require().Equal([]int64{byBaseURL.ID}, s.searchAccountIDs("relay-example.com"), "credentials.base_url, case-insensitive")
	s.Require().Empty(s.searchAccountIDs("sk-secret-value"), "other credential fields are not searched")

	idText := strconv.FormatInt(byID.ID, 10)
	s.Require().Equal([]int64{byID.ID}, s.searchAccountIDs(idText), "exact ID")
	if len(idText) > 1 {
		s.Require().Empty(s.searchAccountIDs(idText[:len(idText)-1]), "an ID prefix must not match by ID")
	}
	s.Require().Equal([]int64{byID.ID}, s.searchAccountIDs("  "+idText+" "), "surrounding whitespace is ignored")
}

func (s *AccountRepoSuite) TestListWithFilters_SearchEscapesLikeWildcards() {
	plain := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "srch-plain",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://plain.test"},
	})
	special := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "srch-special",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://host.test/100%_off"},
	})

	s.Require().Equal([]int64{special.ID}, s.searchAccountIDs("%"), "percent is literal")
	s.Require().Equal([]int64{special.ID}, s.searchAccountIDs("100%"), "percent inside a term is literal")
	s.Require().Equal([]int64{special.ID}, s.searchAccountIDs("_"), "underscore is literal")
	s.Require().Equal([]int64{special.ID}, s.searchAccountIDs("%_o"), "wildcards are not expanded")
	s.Require().Empty(s.searchAccountIDs(`\`), "backslash is literal")
	s.Require().NotContains(s.searchAccountIDs("plain.test"), special.ID)
	s.Require().Contains(s.searchAccountIDs("plain.test"), plain.ID)
}

func (s *AccountRepoSuite) TestListWithFilters_SearchNumericOverflowDoesNotFail() {
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "srch-overflow"})
	s.Require().Empty(s.searchAccountIDs("99999999999999999999999999"))
}

func (s *AccountRepoSuite) TestListIDsWithFilters_ReusesListFilters() {
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "ids-g1"})
	a1 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "ids-a1", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth})
	a2 := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:        "ids-a2",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://ids-relay.example.com"},
	})
	a3 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "ids-a3", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey})
	disabled := mustCreateAccount(s.T(), s.client, &service.Account{Name: "ids-disabled", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusDisabled})
	mustBindAccountToGroup(s.T(), s.client, a1.ID, group.ID, 1)
	mustBindAccountToGroup(s.T(), s.client, a2.ID, group.ID, 1)

	cases := []struct {
		name        string
		platform    string
		accountType string
		status      string
		search      string
		groupID     int64
		wantIDs     []int64
	}{
		{name: "search only", search: "ids-a", wantIDs: []int64{a1.ID, a2.ID, a3.ID}},
		{name: "platform", platform: service.PlatformAnthropic, search: "ids-a", wantIDs: []int64{a1.ID, a3.ID}},
		{name: "type", accountType: service.AccountTypeAPIKey, search: "ids-a", wantIDs: []int64{a2.ID, a3.ID}},
		{name: "group", groupID: group.ID, search: "ids-", wantIDs: []int64{a1.ID, a2.ID}},
		{name: "ungrouped", groupID: service.AccountListGroupUngrouped, search: "ids-a", wantIDs: []int64{a3.ID}},
		{name: "base_url search", search: "ids-relay", wantIDs: []int64{a2.ID}},
		{name: "status", status: service.StatusDisabled, search: "ids-", wantIDs: []int64{disabled.ID}},
	}
	for _, tc := range cases {
		s.Run(tc.name, func() {
			list, err := s.repo.ListIDsWithFilters(s.ctx, tc.platform, tc.accountType, tc.status, tc.search, tc.groupID, "", 100)
			s.Require().NoError(err)

			// 与列表接口命中的集合完全一致。
			accounts, page, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 100}, tc.platform, tc.accountType, tc.status, tc.search, tc.groupID, "")
			s.Require().NoError(err)
			listIDs := make([]int64, 0, len(accounts))
			for _, account := range accounts {
				listIDs = append(listIDs, account.ID)
			}
			sort.Slice(listIDs, func(i, j int) bool { return listIDs[i] < listIDs[j] })
			s.Require().Equal(listIDs, append([]int64{}, list.IDs...))
			s.Require().Equal(page.Total, list.Total)

			s.Require().Equal(tc.wantIDs, list.IDs)
		})
	}
}

func (s *AccountRepoSuite) TestListIDsWithFilters_PlatformsTypesAndOrder() {
	a1 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "idsmeta-b", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	a2 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "idsmeta-a", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth})
	a3 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "idsmeta-c", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey})

	list, err := s.repo.ListIDsWithFilters(s.ctx, "", "", "", "idsmeta-", 0, "", 100)
	s.Require().NoError(err)

	s.Require().Equal([]int64{a1.ID, a2.ID, a3.ID}, list.IDs, "ordered by ID, independent of name")
	s.Require().Equal([]string{service.PlatformAnthropic, service.PlatformOpenAI}, list.Platforms)
	s.Require().Equal([]string{service.AccountTypeAPIKey, service.AccountTypeOAuth}, list.Types)
	s.Require().Equal(int64(3), list.Total)
}

func (s *AccountRepoSuite) TestListIDsWithFilters_LimitExceededReturnsTotalWithoutIDs() {
	for _, name := range []string{"idslimit-a", "idslimit-b", "idslimit-c"} {
		mustCreateAccount(s.T(), s.client, &service.Account{Name: name})
	}

	over, err := s.repo.ListIDsWithFilters(s.ctx, "", "", "", "idslimit-", 0, "", 2)
	s.Require().NoError(err)
	s.Require().Equal(int64(3), over.Total)
	s.Require().Empty(over.IDs, "no IDs are returned when the limit is exceeded")

	exact, err := s.repo.ListIDsWithFilters(s.ctx, "", "", "", "idslimit-", 0, "", 3)
	s.Require().NoError(err)
	s.Require().Equal(int64(3), exact.Total)
	s.Require().Len(exact.IDs, 3, "a result exactly at the limit is returned in full")
}
