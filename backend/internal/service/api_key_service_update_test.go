//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type apiKeyUpdateRepoStub struct {
	apiKeyRepoStub
	updated *APIKey
}

func (s *apiKeyUpdateRepoStub) Update(_ context.Context, key *APIKey) error {
	clone := *key
	s.updated = &clone
	return nil
}

func TestAPIKeyServiceUpdatePreservesIPRulesWhenFieldsAreOmitted(t *testing.T) {
	repo := &apiKeyUpdateRepoStub{apiKeyRepoStub: apiKeyRepoStub{apiKey: &APIKey{
		ID:          7,
		UserID:      11,
		Status:      StatusActive,
		IPWhitelist: []string{"10.0.0.0/8"},
		IPBlacklist: []string{"192.0.2.10"},
	}}}
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, nil, nil)

	_, err := svc.Update(context.Background(), 7, 11, UpdateAPIKeyRequest{})
	require.NoError(t, err)
	require.NotNil(t, repo.updated)
	require.Equal(t, []string{"10.0.0.0/8"}, repo.updated.IPWhitelist)
	require.Equal(t, []string{"192.0.2.10"}, repo.updated.IPBlacklist)
}

func TestAPIKeyServiceUpdateClearsIPRulesWithExplicitEmptyArrays(t *testing.T) {
	repo := &apiKeyUpdateRepoStub{apiKeyRepoStub: apiKeyRepoStub{apiKey: &APIKey{
		ID:          7,
		UserID:      11,
		Status:      StatusActive,
		IPWhitelist: []string{"10.0.0.0/8"},
		IPBlacklist: []string{"192.0.2.10"},
	}}}
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, nil, nil)
	emptyWhitelist := []string{}
	emptyBlacklist := []string{}

	_, err := svc.Update(context.Background(), 7, 11, UpdateAPIKeyRequest{
		IPWhitelist: &emptyWhitelist,
		IPBlacklist: &emptyBlacklist,
	})
	require.NoError(t, err)
	require.NotNil(t, repo.updated)
	require.Empty(t, repo.updated.IPWhitelist)
	require.Empty(t, repo.updated.IPBlacklist)
}

func TestAPIKeyServiceUpdateValidatesAndReplacesNonEmptyIPRules(t *testing.T) {
	repo := &apiKeyUpdateRepoStub{apiKeyRepoStub: apiKeyRepoStub{apiKey: &APIKey{
		ID:          7,
		UserID:      11,
		Status:      StatusActive,
		IPWhitelist: []string{"10.0.0.0/8"},
		IPBlacklist: []string{"192.0.2.10"},
	}}}
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, nil, nil)
	newWhitelist := []string{"203.0.113.0/24"}
	newBlacklist := []string{"198.51.100.25"}

	_, err := svc.Update(context.Background(), 7, 11, UpdateAPIKeyRequest{
		IPWhitelist: &newWhitelist,
		IPBlacklist: &newBlacklist,
	})
	require.NoError(t, err)
	require.NotNil(t, repo.updated)
	require.Equal(t, newWhitelist, repo.updated.IPWhitelist)
	require.Equal(t, newBlacklist, repo.updated.IPBlacklist)
}

func TestAPIKeyServiceUpdateRejectsInvalidIPRules(t *testing.T) {
	invalidWhitelist := []string{"not-an-ip"}
	invalidBlacklist := []string{"also-not-an-ip"}
	cases := []struct {
		name string
		req  UpdateAPIKeyRequest
	}{
		{name: "whitelist", req: UpdateAPIKeyRequest{IPWhitelist: &invalidWhitelist}},
		{name: "blacklist", req: UpdateAPIKeyRequest{IPBlacklist: &invalidBlacklist}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &apiKeyUpdateRepoStub{apiKeyRepoStub: apiKeyRepoStub{apiKey: &APIKey{
				ID:     7,
				UserID: 11,
				Status: StatusActive,
			}}}
			svc := NewAPIKeyService(repo, nil, nil, nil, nil, nil, nil)

			_, err := svc.Update(context.Background(), 7, 11, tc.req)
			require.ErrorIs(t, err, ErrInvalidIPPattern)
			require.Nil(t, repo.updated)
		})
	}
}
