package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newResponsesSequenceContext(t *testing.T, path string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	return c
}

func TestResponsesStreamSequence_ObservesOnlyIncreasingResponsesEvents(t *testing.T) {
	ObserveResponsesStreamSequence(nil, []byte(`{"sequence_number":9}`))

	nonResponses := newResponsesSequenceContext(t, "/v1/chat/completions")
	ObserveResponsesStreamSequence(nonResponses, []byte(`{"sequence_number":9}`))
	require.Equal(t, 0, NextResponsesStreamSequence(nonResponses))

	c := newResponsesSequenceContext(t, "/v1/responses")
	for _, payload := range [][]byte{
		[]byte(`{}`),
		[]byte(`{"sequence_number":"7"}`),
		[]byte(`{"sequence_number":-1}`),
	} {
		ObserveResponsesStreamSequence(c, payload)
	}
	require.Equal(t, 0, NextResponsesStreamSequence(c))

	ObserveResponsesStreamSequence(c, []byte(`{"sequence_number":7}`))
	ObserveResponsesStreamSequence(c, []byte(`{"sequence_number":3}`))
	require.Equal(t, 8, NextResponsesStreamSequence(c))
	require.Equal(t, 9, NextResponsesStreamSequence(c))
}

func TestResponsesStreamSequence_HandlesUnexpectedStoredStateAndNilTracker(t *testing.T) {
	c := newResponsesSequenceContext(t, "/v1/responses")
	c.Set(responsesStreamSequenceNextKey, "invalid")
	require.Equal(t, 0, NextResponsesStreamSequence(c))

	var nilTracker *openAIResponsesSequenceTracker
	nilTracker.Observe([]byte(`{"sequence_number":9}`))
	require.Equal(t, 0, nilTracker.Next())

	tracker := openAIResponsesSequenceTracker{}
	for _, payload := range [][]byte{
		[]byte(`{}`),
		[]byte(`{"sequence_number":"9"}`),
		[]byte(`{"sequence_number":-1}`),
	} {
		tracker.Observe(payload)
	}
	tracker.Observe([]byte(`{"sequence_number":11}`))
	tracker.Observe([]byte(`{"sequence_number":4}`))
	require.Equal(t, 12, tracker.Next())
	require.Equal(t, 13, tracker.Next())
}
