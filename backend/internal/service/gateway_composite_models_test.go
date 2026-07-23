//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type compositeModelsAccountRepoStub struct {
	AccountRepository
	byGroup []Account
	all     []Account
	err     error
}

func (s *compositeModelsAccountRepoStub) ListSchedulableByGroupID(context.Context, int64) ([]Account, error) {
	return s.byGroup, s.err
}

func (s *compositeModelsAccountRepoStub) ListSchedulable(context.Context) ([]Account, error) {
	return s.all, s.err
}

func TestGatewayServiceGetSchedulablePlatforms(t *testing.T) {
	repo := &compositeModelsAccountRepoStub{
		byGroup: []Account{
			{Platform: PlatformOpenAI},
			{Platform: " "},
			{Platform: PlatformGrok},
		},
		all: []Account{{Platform: PlatformGemini}},
	}
	svc := &GatewayService{accountRepo: repo}
	groupID := int64(7)

	require.Equal(t,
		map[string]struct{}{PlatformOpenAI: {}, PlatformGrok: {}},
		svc.GetSchedulablePlatforms(context.Background(), &groupID),
	)
	require.Equal(t,
		map[string]struct{}{PlatformGemini: {}},
		svc.GetSchedulablePlatforms(context.Background(), nil),
	)

	repo.err = errors.New("account repository unavailable")
	require.Empty(t, svc.GetSchedulablePlatforms(context.Background(), &groupID))

	var nilService *GatewayService
	require.Empty(t, nilService.GetSchedulablePlatforms(context.Background(), &groupID))
}
