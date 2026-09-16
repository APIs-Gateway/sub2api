//go:build integration

package repository

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/suite"
)

type SessionLimitCacheSuite struct {
	IntegrationRedisSuite
	cache service.SessionLimitCache
}

func (s *SessionLimitCacheSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	s.cache = NewSessionLimitCache(s.rdb, 5)
}

func (s *SessionLimitCacheSuite) TestUnregisterSession_FreesSlotForNewSession() {
	const accountID = int64(101)
	const maxSessions = 1
	idleTimeout := 5 * time.Minute

	allowed, err := s.cache.RegisterSession(s.ctx, accountID, "session-A", maxSessions, idleTimeout)
	s.Require().NoError(err)
	s.True(allowed)

	allowed, err = s.cache.RegisterSession(s.ctx, accountID, "session-B", maxSessions, idleTimeout)
	s.Require().NoError(err)
	s.False(allowed)

	s.Require().NoError(s.cache.UnregisterSession(s.ctx, accountID, "session-A"))
	allowed, err = s.cache.RegisterSession(s.ctx, accountID, "session-B", maxSessions, idleTimeout)
	s.Require().NoError(err)
	s.True(allowed)

	count, err := s.cache.GetActiveSessionCount(s.ctx, accountID)
	s.Require().NoError(err)
	s.Equal(1, count)
}

func (s *SessionLimitCacheSuite) TestUnregisterSession_IdempotentAndMissing() {
	const accountID = int64(102)

	s.Require().NoError(s.cache.UnregisterSession(s.ctx, accountID, "missing"))
	s.Require().NoError(s.cache.UnregisterSession(s.ctx, accountID, "missing"))
	s.Require().NoError(s.cache.UnregisterSession(s.ctx, accountID, ""))
}

func (s *SessionLimitCacheSuite) TestUnregisterSession_OnlyTargetsSpecifiedSession() {
	const accountID = int64(103)
	const maxSessions = 2
	idleTimeout := 5 * time.Minute

	for _, sessionID := range []string{"session-A", "session-B"} {
		allowed, err := s.cache.RegisterSession(s.ctx, accountID, sessionID, maxSessions, idleTimeout)
		s.Require().NoError(err)
		s.True(allowed)
	}

	s.Require().NoError(s.cache.UnregisterSession(s.ctx, accountID, "session-A"))
	active, err := s.cache.IsSessionActive(s.ctx, accountID, "session-A")
	s.Require().NoError(err)
	s.False(active)
	active, err = s.cache.IsSessionActive(s.ctx, accountID, "session-B")
	s.Require().NoError(err)
	s.True(active)
}

func TestSessionLimitCacheSuite(t *testing.T) {
	suite.Run(t, new(SessionLimitCacheSuite))
}
