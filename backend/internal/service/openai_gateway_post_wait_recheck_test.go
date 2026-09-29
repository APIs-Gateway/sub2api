package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type postWaitMissingAccountRepo struct{ AccountRepository }

func (postWaitMissingAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	return nil, ErrAccountNotFound
}

// A stale snapshot or sticky entry may still contain the paused account after
// the handler releases its slot. The next selection must honor its exclusion.
func TestOpenAIGatewayService_ExcludedPostSlotAccountIsNotReselected(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	account := Account{
		ID:          1396,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
	}
	cfg := &config.Config{}
	cfg.RunMode = config.RunModeSimple
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{account}},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	selected, _, err := svc.SelectAccountWithSchedulerForCapability(
		context.Background(), nil, "", "", "gpt-5.4", nil,
		OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions,
		false, false,
	)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.NotNil(t, selected.Account)
	require.Equal(t, account.ID, selected.Account.ID)
	if selected.ReleaseFunc != nil {
		selected.ReleaseFunc()
	}

	selection, _, err := svc.SelectAccountWithSchedulerForCapability(
		context.Background(), nil, "", "", "gpt-5.4",
		map[int64]struct{}{account.ID: {}},
		OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions,
		false, false,
	)
	require.Nil(t, selection)
	require.Error(t, err)
}

type postWaitFailingAccountRepo struct{ AccountRepository }

func (postWaitFailingAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	return nil, errors.New("database unavailable")
}

func TestOpenAIGatewayService_RecheckAccountSchedulableAfterSlotUsesDB(t *testing.T) {
	selected := Account{
		ID:          1396,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
	}
	for _, tc := range []struct {
		name    string
		repo    AccountRepository
		allowed bool
		wantErr bool
	}{
		{name: "still active", repo: schedulerTestOpenAIAccountRepo{accounts: []Account{selected}}, allowed: true},
		{name: "paused after selection", repo: schedulerTestOpenAIAccountRepo{accounts: []Account{{ID: selected.ID, Status: StatusActive, Schedulable: false}}}},
		{name: "deleted after selection", repo: postWaitMissingAccountRepo{}},
		{name: "database unavailable", repo: postWaitFailingAccountRepo{}, wantErr: true},
		{name: "repository unavailable", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &OpenAIGatewayService{accountRepo: tc.repo}
			allowed, err := svc.RecheckAccountSchedulableAfterSlot(context.Background(), &selected)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.allowed, allowed)
		})
	}
}
