package securityaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type promptEventRepositoryStub struct {
	page                *EventPage
	event               *Event
	listErr             error
	getErr              error
	preview             *DeletePreview
	previewErr          error
	deleteResult        *DeleteResult
	deleteErr           error
	filter              EventFilter
	pageNum             int
	pageSize            int
	requested           int64
	deleteFilter        EventFilter
	deleteSnapshotMaxID int64
}

func (s *promptEventRepositoryStub) ListEvents(_ context.Context, filter EventFilter, page, pageSize int) (*EventPage, error) {
	s.filter, s.pageNum, s.pageSize = filter, page, pageSize
	return s.page, s.listErr
}

func (s *promptEventRepositoryStub) GetEvent(_ context.Context, id int64) (*Event, error) {
	s.requested = id
	return s.event, s.getErr
}

func (s *promptEventRepositoryStub) PreviewDelete(_ context.Context, filter EventFilter) (*DeletePreview, error) {
	s.filter = filter
	return s.preview, s.previewErr
}

func (s *promptEventRepositoryStub) DeleteEventsByFilter(_ context.Context, filter EventFilter, snapshotMaxID int64, _ int) (*DeleteResult, error) {
	s.deleteFilter, s.deleteSnapshotMaxID = filter, snapshotMaxID
	return s.deleteResult, s.deleteErr
}

func newPromptEventTestContext(t *testing.T, target string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	return c, recorder
}

func newPromptEventJSONContext(t *testing.T, target string, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, target, bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, recorder
}

func TestPromptEventAdminHandlerListEventsParsesFilters(t *testing.T) {
	repo := &promptEventRepositoryStub{page: &EventPage{Items: []*Event{}, Total: 0, Page: 2, PageSize: 10}}
	handler := NewPromptEventAdminHandler(repo)
	c, recorder := newPromptEventTestContext(t, "/?page=2&page_size=10&decision=critical&risk_level=high&group_id=4&user_id=2&api_key_id=3&request_id=req-1&prompt_hash=hash&keyword=user&start_at=2023-11-14T22:13:20Z&end_at=2023-11-14T23:13:20Z")

	handler.ListEvents(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 2, repo.pageNum)
	require.Equal(t, 10, repo.pageSize)
	require.Equal(t, "critical", repo.filter.Decision)
	require.Equal(t, "high", repo.filter.RiskLevel)
	require.Equal(t, int64(4), *repo.filter.GroupID)
	require.Equal(t, int64(2), *repo.filter.UserID)
	require.Equal(t, int64(3), *repo.filter.APIKeyID)
	require.Equal(t, "req-1", repo.filter.RequestID)
	require.Equal(t, "hash", repo.filter.PromptHash)
	require.Equal(t, "user", repo.filter.Keyword)
	require.Equal(t, time.Unix(1700000000, 0).UTC(), *repo.filter.StartAt)
}

func TestPromptEventAdminHandlerRejectsInvalidQuery(t *testing.T) {
	for _, target := range []string{"/?page=0", "/?page_size=101", "/?group_id=0", "/?user_id=0", "/?api_key_id=0", "/?start_at=not-a-time", "/?end_at=not-a-time"} {
		t.Run(target, func(t *testing.T) {
			repo := &promptEventRepositoryStub{page: &EventPage{}}
			handler := NewPromptEventAdminHandler(repo)
			c, recorder := newPromptEventTestContext(t, target)

			handler.ListEvents(c)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Zero(t, repo.pageNum)
		})
	}
}

func TestPromptEventAdminHandlerListEventsMapsRepositoryErrors(t *testing.T) {
	repo := &promptEventRepositoryStub{listErr: errors.New("list failed")}
	handler := NewPromptEventAdminHandler(repo)
	c, recorder := newPromptEventTestContext(t, "/")
	handler.ListEvents(c)
	require.Equal(t, http.StatusInternalServerError, recorder.Code)

	c, recorder = newPromptEventTestContext(t, "/")
	(*PromptEventAdminHandler)(nil).ListEvents(c)
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
}

func TestPromptEventAdminHandlerGetEventMapsNotFoundAndOmitsRawText(t *testing.T) {
	event := &Event{ID: 21, Snapshot: PromptSnapshot{RedactedPreview: "redacted", ScanText: "must not be serialized"}}
	repo := &promptEventRepositoryStub{event: event}
	handler := NewPromptEventAdminHandler(repo)
	c, recorder := newPromptEventTestContext(t, "/admin/prompt-audit/events/21")
	c.Params = gin.Params{{Key: "id", Value: "21"}}

	handler.GetEvent(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, int64(21), repo.requested)
	require.Contains(t, recorder.Body.String(), "redacted")
	require.NotContains(t, recorder.Body.String(), "must not be serialized")
	require.NotContains(t, recorder.Body.String(), "full_prompt")

	repo.getErr = ErrEventNotFound
	c, recorder = newPromptEventTestContext(t, "/admin/prompt-audit/events/21")
	c.Params = gin.Params{{Key: "id", Value: "21"}}
	handler.GetEvent(c)
	require.Equal(t, http.StatusNotFound, recorder.Code)
}

func TestPromptEventAdminHandlerRejectsInvalidEventID(t *testing.T) {
	repo := &promptEventRepositoryStub{getErr: errors.New("should not be called")}
	handler := NewPromptEventAdminHandler(repo)
	c, recorder := newPromptEventTestContext(t, "/admin/prompt-audit/events/nope")
	c.Params = gin.Params{{Key: "id", Value: "nope"}}

	handler.GetEvent(c)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Zero(t, repo.requested)
}

func TestPromptEventAdminHandlerGetEventMapsRepositoryErrors(t *testing.T) {
	repo := &promptEventRepositoryStub{getErr: errors.New("get failed")}
	handler := NewPromptEventAdminHandler(repo)
	c, recorder := newPromptEventTestContext(t, "/admin/prompt-audit/events/21")
	c.Params = gin.Params{{Key: "id", Value: "21"}}
	handler.GetEvent(c)
	require.Equal(t, http.StatusInternalServerError, recorder.Code)

	c, recorder = newPromptEventTestContext(t, "/admin/prompt-audit/events/21")
	c.Params = gin.Params{{Key: "id", Value: "21"}}
	(*PromptEventAdminHandler)(nil).GetEvent(c)
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
}

func TestPromptEventAdminHandlerResponseEnvelopeIsJSON(t *testing.T) {
	repo := &promptEventRepositoryStub{page: &EventPage{Items: []*Event{}, Total: 0, Page: 1, PageSize: 20}}
	handler := NewPromptEventAdminHandler(repo)
	c, recorder := newPromptEventTestContext(t, "/")
	handler.ListEvents(c)

	var envelope struct {
		Code int       `json:"code"`
		Data EventPage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Zero(t, envelope.Code)
	require.Equal(t, 20, envelope.Data.PageSize)
}

type promptEventHandlerTestClock struct{ now time.Time }

func (c promptEventHandlerTestClock) Now() time.Time { return c.now }

func TestPromptEventAdminHandlerFilterDeleteRequiresBoundSingleUseConfirmation(t *testing.T) {
	start := time.Unix(1700000000, 0).UTC()
	end := start.Add(time.Hour)
	preview := &DeletePreview{
		MatchedCount: 2, FilterSummary: EventFilter{StartAt: &start, EndAt: &end}, SnapshotMaxID: 42,
		FilterHash: FilterHash(EventFilter{StartAt: &start, EndAt: &end}, 42),
	}
	repo := &promptEventRepositoryStub{preview: preview, deleteResult: &DeleteResult{DeletedEvents: 2}}
	handler := NewPromptEventAdminHandler(repo)
	handler.clock = promptEventHandlerTestClock{now: start}

	c, recorder := newPromptEventJSONContext(t, "/admin/prompt-audit/events/delete-preview", `{"start_at":"2023-11-14T22:13:20Z","end_at":"2023-11-14T23:13:20Z"}`)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
	handler.DeletePreview(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotEmpty(t, preview.ConfirmationToken)
	require.Equal(t, start.Add(5*time.Minute), preview.ExpiresAt)

	request := DeleteByFilterRequest{Filter: EventFilter{StartAt: &start, EndAt: &end}, SnapshotMaxID: 42, FilterHash: preview.FilterHash, ConfirmationToken: preview.ConfirmationToken, Confirm: true}
	requestBody, err := json.Marshal(request)
	require.NoError(t, err)
	c, recorder = newPromptEventJSONContext(t, "/admin/prompt-audit/events/delete-by-filter", string(requestBody))
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
	handler.DeleteByFilter(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, int64(42), repo.deleteSnapshotMaxID)
	require.Equal(t, request.Filter, repo.deleteFilter)
	require.Error(t, handler.consumeConfirmation(request, 0), "a consumed token must not be replayable")

	otherAdminPreview := &DeletePreview{MatchedCount: 1, FilterSummary: EventFilter{StartAt: &start, EndAt: &end}, SnapshotMaxID: 42, FilterHash: preview.FilterHash}
	require.NoError(t, handler.issueConfirmation(otherAdminPreview, 9))
	request.ConfirmationToken = otherAdminPreview.ConfirmationToken
	require.Error(t, handler.consumeConfirmation(request, 0), "confirmation tokens must be bound to the issuing admin")
}

func TestPromptEventAdminHandlerFilterDeleteRejectsMalformedAndExpiredConfirmation(t *testing.T) {
	start := time.Unix(1700000000, 0).UTC()
	end := start.Add(time.Hour)
	repo := &promptEventRepositoryStub{deleteResult: &DeleteResult{}}
	handler := NewPromptEventAdminHandler(repo)
	handler.clock = promptEventHandlerTestClock{now: end}

	c, recorder := newPromptEventJSONContext(t, "/admin/prompt-audit/events/delete-by-filter", `{"filter":{"start_at":"2023-11-14T22:13:20Z"},"snapshot_max_id":42,"filter_hash":"hash","confirmation_token":"missing","confirm":true}`)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
	handler.DeleteByFilter(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Zero(t, repo.deleteSnapshotMaxID)

	preview := &DeletePreview{MatchedCount: 1, FilterSummary: EventFilter{StartAt: &start, EndAt: &end}, SnapshotMaxID: 42, FilterHash: FilterHash(EventFilter{StartAt: &start, EndAt: &end}, 42)}
	require.NoError(t, handler.issueConfirmation(preview, 0))
	handler.clock = promptEventHandlerTestClock{now: end.Add(6 * time.Minute)}
	request, err := json.Marshal(DeleteByFilterRequest{Filter: EventFilter{StartAt: &start, EndAt: &end}, SnapshotMaxID: 42, FilterHash: preview.FilterHash, ConfirmationToken: preview.ConfirmationToken, Confirm: true})
	require.NoError(t, err)
	c, recorder = newPromptEventJSONContext(t, "/admin/prompt-audit/events/delete-by-filter", string(request))
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
	handler.DeleteByFilter(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Zero(t, repo.deleteSnapshotMaxID)
}
