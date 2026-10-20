package securityaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
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
	deleteFn            func(context.Context, EventFilter, int64, int) (*DeleteResult, error)
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

func (s *promptEventRepositoryStub) DeleteEventsByFilter(ctx context.Context, filter EventFilter, snapshotMaxID int64, batchSize int) (*DeleteResult, error) {
	if s.deleteFn != nil {
		return s.deleteFn(ctx, filter, snapshotMaxID, batchSize)
	}
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

func TestPromptEventAdminHandlerDeletePreviewMapsValidationRepositoryAndAuthenticationFailures(t *testing.T) {
	start := time.Unix(1700000000, 0).UTC()
	end := start.Add(time.Hour)
	preview := &DeletePreview{
		MatchedCount: 1, FilterSummary: EventFilter{StartAt: &start, EndAt: &end}, SnapshotMaxID: 42,
		FilterHash: FilterHash(EventFilter{StartAt: &start, EndAt: &end}, 42),
	}

	for _, test := range []struct {
		name       string
		body       string
		previewErr error
		auth       bool
		want       int
	}{
		{name: "malformed body", body: `{`, want: http.StatusBadRequest},
		{name: "invalid filter", body: `{}`, previewErr: ErrInvalidDeleteFilter, auth: true, want: http.StatusBadRequest},
		{name: "zero matches", body: `{}`, previewErr: ErrDeleteNoMatches, auth: true, want: http.StatusBadRequest},
		{name: "repository failure", body: `{}`, previewErr: errors.New("preview failed"), auth: true, want: http.StatusInternalServerError},
		{name: "missing admin identity", body: `{}`, want: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &promptEventRepositoryStub{preview: preview, previewErr: test.previewErr}
			handler := NewPromptEventAdminHandler(repo)
			c, recorder := newPromptEventJSONContext(t, "/admin/prompt-audit/events/delete-preview", test.body)
			if test.auth {
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
			}

			handler.DeletePreview(c)

			require.Equal(t, test.want, recorder.Code)
		})
	}
}

func TestPromptEventAdminHandlerConcurrentTokensRejectStaleConfirmation(t *testing.T) {
	start := time.Unix(1700000000, 0).UTC()
	end := start.Add(time.Hour)
	filter := EventFilter{StartAt: &start, EndAt: &end}
	filterHash := FilterHash(filter, 42)

	var arrived sync.WaitGroup
	arrived.Add(2)
	var deletionMu sync.Mutex
	deleted := false
	repo := &promptEventRepositoryStub{
		deleteFn: func(_ context.Context, _ EventFilter, _ int64, _ int) (*DeleteResult, error) {
			arrived.Done()
			arrived.Wait()

			deletionMu.Lock()
			defer deletionMu.Unlock()
			if deleted {
				return nil, ErrDeleteNoMatches
			}
			deleted = true
			return &DeleteResult{DeletedEvents: 1}, nil
		},
	}
	handler := NewPromptEventAdminHandler(repo)
	handler.clock = promptEventHandlerTestClock{now: start}
	first := &DeletePreview{MatchedCount: 1, FilterSummary: filter, SnapshotMaxID: 42, FilterHash: filterHash}
	second := &DeletePreview{MatchedCount: 1, FilterSummary: filter, SnapshotMaxID: 42, FilterHash: filterHash}
	require.NoError(t, handler.issueConfirmation(first, 7))
	require.NoError(t, handler.issueConfirmation(second, 7))

	type confirmationCall struct {
		context  *gin.Context
		recorder *httptest.ResponseRecorder
	}
	calls := make([]confirmationCall, 0, 2)
	for _, token := range []string{first.ConfirmationToken, second.ConfirmationToken} {
		requestBody, err := json.Marshal(DeleteByFilterRequest{
			Filter: filter, SnapshotMaxID: 42, FilterHash: filterHash, ConfirmationToken: token, Confirm: true,
		})
		require.NoError(t, err)
		c, recorder := newPromptEventJSONContext(t, "/admin/prompt-audit/events/delete-by-filter", string(requestBody))
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
		calls = append(calls, confirmationCall{context: c, recorder: recorder})
	}

	statuses := make(chan int, len(calls))
	var confirmations sync.WaitGroup
	for _, call := range calls {
		confirmations.Add(1)
		go func(call confirmationCall) {
			defer confirmations.Done()
			handler.DeleteByFilter(call.context)
			statuses <- call.recorder.Code
		}(call)
	}
	confirmations.Wait()
	close(statuses)

	counts := make(map[int]int)
	for status := range statuses {
		counts[status]++
	}
	require.Equal(t, 1, counts[http.StatusOK])
	require.Equal(t, 1, counts[http.StatusBadRequest])
}

func TestPromptEventAdminHandlerDeleteByFilterPropagatesRepositoryFailure(t *testing.T) {
	start := time.Unix(1700000000, 0).UTC()
	end := start.Add(time.Hour)
	filter := EventFilter{StartAt: &start, EndAt: &end}
	filterHash := FilterHash(filter, 42)
	repo := &promptEventRepositoryStub{deleteErr: errors.New("delete failed")}
	handler := NewPromptEventAdminHandler(repo)
	handler.clock = promptEventHandlerTestClock{now: start}
	preview := &DeletePreview{MatchedCount: 1, FilterSummary: filter, SnapshotMaxID: 42, FilterHash: filterHash}
	require.NoError(t, handler.issueConfirmation(preview, 7))
	request, err := json.Marshal(DeleteByFilterRequest{
		Filter: filter, SnapshotMaxID: 42, FilterHash: filterHash, ConfirmationToken: preview.ConfirmationToken, Confirm: true,
	})
	require.NoError(t, err)

	c, recorder := newPromptEventJSONContext(t, "/admin/prompt-audit/events/delete-by-filter", string(request))
	handler.DeleteByFilter(c)
	require.Equal(t, http.StatusUnauthorized, recorder.Code)

	c, recorder = newPromptEventJSONContext(t, "/admin/prompt-audit/events/delete-by-filter", string(request))
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
	handler.DeleteByFilter(c)

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
}
