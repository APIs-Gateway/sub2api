package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type invitationRedeemRepoStub struct {
	RedeemCodeRepository
	code RedeemCode
}

func (r *invitationRedeemRepoStub) GetByCode(context.Context, string) (*RedeemCode, error) {
	copy := r.code
	return &copy, nil
}

func TestRedeemRejectsInvitationCodeBeforeTransaction(t *testing.T) {
	repo := &invitationRedeemRepoStub{code: RedeemCode{
		ID: 1, Code: "INVITE-001", Type: RedeemTypeInvitation, Status: StatusUnused,
	}}
	svc := NewRedeemService(repo, nil, nil, nil, nil, nil, nil, nil)

	got, err := svc.Redeem(context.Background(), 2, repo.code.Code)

	require.Nil(t, got)
	require.Error(t, err)
	require.True(t, infraerrors.IsBadRequest(err))
	require.Equal(t, "REDEEM_CODE_UNSUPPORTED_TYPE", infraerrors.Reason(err))
	require.Equal(t, "invitation codes can only be used during registration", infraerrors.Message(err))
}

func TestUnsupportedRedeemTypeError(t *testing.T) {
	err := unsupportedRedeemTypeError("unknown")
	require.True(t, infraerrors.IsBadRequest(err))
	require.Equal(t, "REDEEM_CODE_UNSUPPORTED_TYPE", infraerrors.Reason(err))
	require.Equal(t, "unsupported redeem type: unknown", infraerrors.Message(err))
}

func TestRedeemRejectsSubscriptionWithoutGroupBeforeTransaction(t *testing.T) {
	repo := &invitationRedeemRepoStub{code: RedeemCode{
		ID: 2, Code: "SUB-001", Type: RedeemTypeSubscription, Status: StatusUnused,
	}}
	svc := NewRedeemService(repo, nil, nil, nil, nil, nil, nil, nil)

	got, err := svc.Redeem(context.Background(), 2, repo.code.Code)

	require.Nil(t, got)
	require.True(t, infraerrors.IsBadRequest(err))
	require.Equal(t, "REDEEM_CODE_INVALID", infraerrors.Reason(err))
}

func TestInvalidSubscriptionRedeemPreflight(t *testing.T) {
	groupID := int64(7)
	for _, tt := range []struct {
		name string
		code *RedeemCode
		want bool
	}{
		{
			name: "rejects missing daily amount and group",
			code: &RedeemCode{Type: RedeemTypeSubscription},
			want: true,
		},
		{
			name: "allows no group code with direct daily amount",
			code: &RedeemCode{Type: RedeemTypeSubscription, Value: 25},
		},
		{
			name: "allows negative validity reduction without group",
			code: &RedeemCode{Type: RedeemTypeSubscription, ValidityDays: -1},
		},
		{
			name: "allows legacy group fallback",
			code: &RedeemCode{Type: RedeemTypeSubscription, GroupID: &groupID},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, invalidSubscriptionRedeemPreflight(tt.code))
		})
	}
}
