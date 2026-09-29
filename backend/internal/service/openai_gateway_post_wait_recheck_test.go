package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type postWaitMissingAccountRepo struct{ AccountRepository }

func (postWaitMissingAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	return nil, ErrAccountNotFound
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
