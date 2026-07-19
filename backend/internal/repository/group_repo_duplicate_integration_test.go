//go:build integration

package repository

import (
	"context"
	"testing"

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

	duplicate := &service.Group{
		Name:                 "duplicate-source (Copy)",
		Platform:             source.Platform,
		RateMultiplier:       source.RateMultiplier,
		Status:               "inactive",
		SubscriptionType:     source.SubscriptionType,
		DuplicateOperationID: "digest-123",
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
