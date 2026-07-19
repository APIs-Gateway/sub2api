package securityaudit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type promptEventRepositoryStub struct {
	page      *EventPage
	event     *Event
	listErr   error
	getErr    error
	filter    EventFilter
	pageNum   int
	pageSize  int
	requested int64
}

func (s *promptEventRepositoryStub) ListEvents(_ context.Context, filter EventFilter, page, pageSize int) (*EventPage, error) {
	s.filter, s.pageNum, s.pageSize = filter, page, pageSize
	return s.page, s.listErr
}

func (s *promptEventRepositoryStub) GetEvent(_ context.Context, id int64) (*Event, error) {
	s.requested = id
	return s.event, s.getErr
}

func newPromptEventTestContext(t *testing.T, target string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
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
