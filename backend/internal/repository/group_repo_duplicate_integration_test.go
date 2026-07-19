//go:build integration

package repository

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *GroupRepoSuite) TestCreateFromSourceCopiesPrioritiesAndOperationMetadata() {
	source := &service.Group{
		Name:             "duplicate-source",
		Platform:         service.PlatformOpenAI,
		RateMultiplier:   0.75,
		Status:           service.StatusActive,
		SubscriptionType: service.SubscriptionTypeStandard,
	}
	s.Require().NoError(s.repo.Create(s.ctx, source))

	accountIDs := make([]int64, 0, 2)
	for _, name := range []string{"duplicate-account-a", "duplicate-account-b"} {
		var accountID int64
		err := scanSingleRow(s.ctx, s.tx,
			"INSERT INTO accounts (name, platform, type) VALUES ($1, $2, $3) RETURNING id",
			[]any{name, service.PlatformOpenAI, service.AccountTypeOAuth}, &accountID,
		)
		s.Require().NoError(err)
		accountIDs = append(accountIDs, accountID)
	}
	_, err := s.tx.ExecContext(s.ctx,
		"INSERT INTO account_groups (account_id, group_id, priority, created_at) VALUES ($1, $2, $3, NOW()), ($4, $2, $5, NOW())",
		accountIDs[0], source.ID, 4, accountIDs[1], 9,
	)
	s.Require().NoError(err)

	description := "copied config"
	dailyLimit, weeklyLimit, monthlyLimit := 1.25, 2.5, 3.75
	imagePrice1K, imagePrice2K, imagePrice4K := 0.1, 0.2, 0.4
	fallbackID, invalidFallbackID, stableFallbackID := source.ID, source.ID, source.ID
	duplicate := &service.Group{
		Name:                            "duplicate-source (Copy)",
		Description:                     description,
		Platform:                        source.Platform,
		RateMultiplier:                  source.RateMultiplier,
		IsExclusive:                     true,
		Status:                          "inactive",
		SubscriptionType:                source.SubscriptionType,
		DailyLimitUSD:                   &dailyLimit,
		WeeklyLimitUSD:                  &weeklyLimit,
		MonthlyLimitUSD:                 &monthlyLimit,
		DefaultValidityDays:             30,
		AllowImageGeneration:            true,
		ImageRateIndependent:            true,
		ImageRateMultiplier:             1.5,
		ImagePrice1K:                    &imagePrice1K,
		ImagePrice2K:                    &imagePrice2K,
		ImagePrice4K:                    &imagePrice4K,
		ClaudeCodeOnly:                  true,
		FallbackGroupID:                 &fallbackID,
		FallbackGroupIDOnInvalidRequest: &invalidFallbackID,
		StablePriorityFallbackGroupID:   &stableFallbackID,
		ModelRouting:                    map[string][]int64{"gpt-5": {11, 12}},
		ModelRoutingEnabled:             true,
		MCPXMLInject:                    true,
		SupportedModelScopes:            []string{"claude", "gemini_text"},
		SortOrder:                       4,
		AllowMessagesDispatch:           true,
		RequireOAuthOnly:                true,
		RequirePrivacySet:               true,
		DefaultMappedModel:              "gpt-5.4",
		MessagesDispatchModelConfig:     service.OpenAIMessagesDispatchModelConfig{ExactModelMappings: map[string]string{"claude": "gpt-5.4"}},
		ModelsListConfig:                service.GroupModelsListConfig{Enabled: true, Models: []string{"gpt-5.4"}},
		RPMLimit:                        20,
		DuplicateOperationID:            "digest-123",
	}
	s.Require().NoError(s.repo.CreateFromSource(context.Background(), duplicate, source.ID))

	got, err := s.repo.GetByID(s.ctx, duplicate.ID)
	s.Require().NoError(err)
	s.Require().Equal("digest-123", got.DuplicateOperationID)
	s.Require().Equal(int64(2), got.AccountCount)

	rows, err := s.tx.QueryContext(s.ctx,
		"SELECT account_id, priority FROM account_groups WHERE group_id = $1 ORDER BY priority, account_id",
		duplicate.ID,
	)
	s.Require().NoError(err)
	defer rows.Close()
	var priorities []int
	var copiedIDs []int64
	for rows.Next() {
		var accountID int64
		var priority int
		s.Require().NoError(rows.Scan(&accountID, &priority))
		copiedIDs = append(copiedIDs, accountID)
		priorities = append(priorities, priority)
	}
	s.Require().NoError(rows.Err())
	s.Require().Equal(accountIDs, copiedIDs)
	s.Require().Equal([]int{4, 9}, priorities)

	recovered, err := s.repo.FindByDuplicateOperationID(s.ctx, "digest-123")
	s.Require().NoError(err)
	s.Require().Equal(duplicate.ID, recovered.ID)
	s.Require().Equal(description, recovered.Description)
	s.Require().Equal(30, recovered.DefaultValidityDays)
	s.Require().Equal(map[string][]int64{"gpt-5": {11, 12}}, recovered.ModelRouting)
	s.Require().Equal(service.GroupModelsListConfig{Enabled: true, Models: []string{"gpt-5.4"}}, recovered.ModelsListConfig)
}

func (s *GroupRepoSuite) TestCreateFromSourceRejectsNilDuplicate() {
	err := s.repo.CreateFromSource(s.ctx, nil, 1)
	s.Require().ErrorContains(err, "group is nil")
}

func (s *GroupRepoSuite) TestCreateFromSourceReturnsNameConflict() {
	source := &service.Group{
		Name:             "duplicate-conflict-source",
		Platform:         service.PlatformOpenAI,
		RateMultiplier:   1,
		Status:           service.StatusActive,
		SubscriptionType: service.SubscriptionTypeStandard,
	}
	s.Require().NoError(s.repo.Create(s.ctx, source))

	duplicate := &service.Group{
		Name:                 source.Name,
		Platform:             source.Platform,
		RateMultiplier:       1,
		Status:               "inactive",
		SubscriptionType:     service.SubscriptionTypeStandard,
		DuplicateOperationID: "conflict-op",
	}
	err := s.repo.CreateFromSource(s.ctx, duplicate, source.ID)
	s.Require().ErrorIs(err, service.ErrGroupExists)
}

func (s *GroupRepoSuite) TestFindByDuplicateOperationIDHandlesEmptyAndMissingValues() {
	got, err := s.repo.FindByDuplicateOperationID(s.ctx, "  ")
	s.Require().NoError(err)
	s.Require().Nil(got)

	got, err = s.repo.FindByDuplicateOperationID(s.ctx, "not-present")
	s.Require().NoError(err)
	s.Require().Nil(got)
}

func (s *GroupRepoSuite) TestCreateFromSourceMissingSourceDoesNotCreateGroup() {
	duplicate := &service.Group{
		Name:                 "orphan-copy",
		Platform:             service.PlatformOpenAI,
		RateMultiplier:       1,
		Status:               "inactive",
		SubscriptionType:     service.SubscriptionTypeStandard,
		DuplicateOperationID: "missing-source-op",
	}
	err := s.repo.CreateFromSource(s.ctx, duplicate, 9_999_999)
	s.Require().ErrorIs(err, service.ErrGroupNotFound)

	var count int
	s.Require().NoError(scanSingleRow(
		s.ctx,
		s.tx,
		"SELECT COUNT(*) FROM groups WHERE name = $1 AND deleted_at IS NULL",
		[]any{"orphan-copy"},
		&count,
	))
	s.Require().Zero(count)
}
