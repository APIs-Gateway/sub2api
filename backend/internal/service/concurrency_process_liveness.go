package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	processHeartbeatInterval = 10 * time.Second
	processHeartbeatTimeout  = 3 * time.Second
)

// ProcessLivenessCache is an optional extension for caches shared by processes.
type ProcessLivenessCache interface {
	HeartbeatProcess(ctx context.Context, requestPrefix string) error
}

func (s *ConcurrencyService) heartbeatProcess(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if liveness, ok := s.cache.(ProcessLivenessCache); ok {
		return liveness.HeartbeatProcess(ctx, RequestIDPrefix())
	}
	return nil
}

func (s *ConcurrencyService) StartProcessHeartbeat() {
	if s == nil {
		return
	}
	if _, ok := s.cache.(ProcessLivenessCache); !ok {
		return
	}
	go func() {
		ticker := time.NewTicker(processHeartbeatInterval)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), processHeartbeatTimeout)
			err := s.heartbeatProcess(ctx)
			cancel()
			if err != nil {
				logger.LegacyPrintf("service.concurrency", "Warning: process heartbeat failed: %v", err)
			}
		}
	}()
}
