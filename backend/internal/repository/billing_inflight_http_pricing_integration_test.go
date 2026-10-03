//go:build integration

package repository

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func inflightRealResponsesRequest(f *wsInflightFixture, body string) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)).WithContext(ctx))
		done <- rec
	}()
	return done
}

func inflightRealResponsesDone(t *testing.T, done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case rec := <-done:
		return rec
	case <-time.After(12 * time.Second):
		t.Fatal("real Responses handler did not complete")
		return nil
	}
}

func TestBillingInflightHTTP_RealProviderAuthFailoverDoesNotStackFunding(t *testing.T) {
	f := newWSInflightFixture(t, "bridge", service.BillingModelSourceUpstream, map[string]float64{"gpt-5.4": .5})
	f.addAccount(t)
	f.provider.fault.Store("401")
	done := inflightRealResponsesRequest(f, `{"model":"gpt-5.4","input":"hello"}`)
	first := f.provider.next(t)
	require.InDelta(t, .5, f.held(t), 1e-9)
	close(first.release)
	second := f.provider.next(t)
	require.InDelta(t, .5, f.held(t), 1e-9, "complete refused account releases only its zero-cost attempt")
	f.provider.fault.Store("")
	close(second.release)
	rec := inflightRealResponsesDone(t, done)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	f.waitUsage(t, 1)
	require.InDelta(t, .25, f.wallet(t), 1e-9)
	require.Zero(t, f.held(t))
	require.EqualValues(t, 1, f.billingRepo.calls.Load())
	require.EqualValues(t, 2, f.provider.calls.Load())
}

func TestBillingInflightHTTP_RealBlockedMismatchAuditDoesNotStackFunding(t *testing.T) {
	f := newWSInflightFixture(t, "bridge", service.BillingModelSourceUpstream, map[string]float64{"gpt-5.4": .5})
	f.addAccount(t)
	f.provider.fault.Store("mismatch")
	done := inflightRealResponsesRequest(f, `{"model":"gpt-5.4","input":"requested model required"}`)
	first := f.provider.next(t)
	require.InDelta(t, .5, f.held(t), 1e-9)
	close(first.release)
	second := f.provider.next(t)
	require.InDelta(t, .5, f.held(t), 1e-9, "zero-cost blocked audit must not keep the old estimate alongside retry")
	close(second.release)
	rec := inflightRealResponsesDone(t, done)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	f.waitUsage(t, 2)
	require.InDelta(t, .25, f.wallet(t), 1e-9)
	require.Zero(t, f.held(t))
	require.EqualValues(t, 1, f.billingRepo.calls.Load())
	var audit int
	var cost float64
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*),COALESCE(sum(actual_cost),0) FROM usage_logs WHERE user_id=$1 AND upstream_model_mismatch=true`, f.userID).Scan(&audit, &cost))
	require.Equal(t, 1, audit)
	require.Zero(t, cost)
	ok, err := NewUsageBillingRepository(testEntClient(t), integrationDB).(service.BillingInflightRepository).ReserveBillingInflight(context.Background(), f.userID, uuid.NewString(), .25, false, time.Minute)
	require.NoError(t, err)
	require.True(t, ok, "third owner sees only settled cost, not a stranded mismatch estimate")
}

func TestBillingInflightHTTP_RealFreeTextAndPaidImageFunding(t *testing.T) {
	for _, exclusive := range []bool{false, true} {
		name := "held_wallet"
		if exclusive {
			name = "unknown_exclusive"
		}
		t.Run(name, func(t *testing.T) {
			f := newWSInflightFixture(t, "bridge", service.BillingModelSourceUpstream, map[string]float64{"gpt-5.4": 0, "gpt-image-2": .5})
			repo := NewUsageBillingRepository(testEntClient(t), integrationDB).(service.BillingInflightRepository)
			owner := uuid.NewString()
			amount := .75
			if exclusive {
				amount = 0
			}
			ok, err := repo.ReserveBillingInflight(context.Background(), f.userID, owner, amount, exclusive, time.Minute)
			require.NoError(t, err)
			require.True(t, ok)
			done := inflightRealResponsesRequest(f, `{"model":"gpt-5.4","input":"free text"}`)
			turn := f.provider.next(t)
			require.InDelta(t, amount, f.held(t), 1e-9, "known-free text never consumes or conflicts with paid/exclusive funding")
			close(turn.release)
			rec := inflightRealResponsesDone(t, done)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			f.waitUsage(t, 1)
			require.InDelta(t, .75, f.wallet(t), 1e-9)
			require.NoError(t, repo.ReleaseBillingInflight(context.Background(), f.userID, owner))
			done = inflightRealResponsesRequest(f, `{"model":"gpt-5.4","input":"draw","tools":[{"type":"image_generation","model":"gpt-image-2","size":"1024x1024"}]}`)
			imageTurn := f.provider.next(t)
			require.InDelta(t, .5, f.held(t), 1e-9, "free chat channel cannot make generated images free")
			ok, err = repo.ReserveBillingInflight(context.Background(), f.userID, uuid.NewString(), .5, false, time.Minute)
			require.NoError(t, err)
			require.False(t, ok)
			close(imageTurn.release)
			rec = inflightRealResponsesDone(t, done)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			f.waitUsage(t, 2)
			require.InDelta(t, .25, f.wallet(t), 1e-9)
			require.Zero(t, f.held(t))
		})
	}
}
