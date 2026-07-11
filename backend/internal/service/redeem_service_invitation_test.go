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
