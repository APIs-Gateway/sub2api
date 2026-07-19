//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

type duplicateGroupRepoStub struct {
	*groupRepoStubForAdmin
	created     *Group
	createErr   error
	createCalls int
}

func (s *duplicateGroupRepoStub) FindByDuplicateOperationID(_ context.Context, operationID string) (*Group, error) {
	if s.created != nil && s.created.DuplicateOperationID == operationID {
		return s.created, nil
	}
	return nil, nil
}

func (s *duplicateGroupRepoStub) CreateFromSource(_ context.Context, duplicate *Group, _ int64) error {
	s.createCalls++
	if s.createErr != nil {
		return s.createErr
	}
	duplicate.ID = 99
	duplicate.AccountCount = 2
	s.created = duplicate
	return nil
}

func (s *duplicateGroupRepoStub) GetByID(_ context.Context, id int64) (*Group, error) {
	if s.created != nil && id == s.created.ID {
		return s.created, nil
	}
	return s.groupRepoStubForAdmin.GetByID(context.Background(), id)
}

func TestDuplicateGroupCopiesCurrentForkConfigurationAndRecovers(t *testing.T) {
	dailyLimit := 1.25
	modelRouting := map[string][]int64{"gpt-5": {11, 12}}
	source := &Group{
		ID:                   7,
		Name:                 "production",
		Description:          "primary",
		Platform:             PlatformOpenAI,
		RateMultiplier:       0.8,
		IsExclusive:          true,
		Status:               StatusActive,
		SubscriptionType:     SubscriptionTypeStandard,
		DailyLimitUSD:        &dailyLimit,
		ModelRouting:         modelRouting,
		ModelRoutingEnabled:  true,
		SupportedModelScopes: []string{"claude", "gemini_text"},
		RequireOAuthOnly:     true,
		RequirePrivacySet:    true,
		DefaultMappedModel:   "gpt-5",
		RPMLimit:             20,
	}
	repo := &duplicateGroupRepoStub{
		groupRepoStubForAdmin: &groupRepoStubForAdmin{getByID: source},
	}
	svc := &adminServiceImpl{groupRepo: repo}

	first, err := svc.DuplicateGroup(context.Background(), source.ID, "admin:42", "copy-1")
	require.NoError(t, err)
	require.Equal(t, 1, repo.createCalls)
	require.Equal(t, "production (Copy)", first.Name)
	require.Equal(t, "inactive", first.Status)
	require.Equal(t, source.Description, first.Description)
	require.Equal(t, source.Platform, first.Platform)
	require.Equal(t, source.RPMLimit, first.RPMLimit)
	require.Equal(t, source.ModelRouting, first.ModelRouting)
	require.NotEqual(t, fmt.Sprintf("%p", source.ModelRouting), fmt.Sprintf("%p", first.ModelRouting))
	require.NotEmpty(t, first.DuplicateOperationID)

	second, err := svc.DuplicateGroup(context.Background(), source.ID, "admin:42", "copy-1")
	require.NoError(t, err)
	require.Equal(t, 1, repo.createCalls)
	require.Equal(t, first.ID, second.ID)
}

func TestDuplicateGroupNameUsesRuneLimitAndSequence(t *testing.T) {
	name := "分组" + strings.Repeat("x", 120)
	first := duplicateGroupName(name, 1)
	second := duplicateGroupName(name, 2)
	require.Equal(t, 100, utf8.RuneCountInString(first))
	require.Equal(t, 100, utf8.RuneCountInString(second))
	require.True(t, strings.HasSuffix(first, " (Copy)"))
	require.True(t, strings.HasSuffix(second, " (Copy 2)"))
}

func TestDuplicateGroupPropagatesNonConflictError(t *testing.T) {
	repo := &duplicateGroupRepoStub{
		groupRepoStubForAdmin: &groupRepoStubForAdmin{getByID: &Group{ID: 1, Name: "source"}},
		createErr:             errors.New("binding failed"),
	}
	svc := &adminServiceImpl{groupRepo: repo}

	_, err := svc.DuplicateGroup(context.Background(), 1, "admin:1", "copy")
	require.ErrorContains(t, err, "binding failed")
}
