package websearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTavilyProvider_Name(t *testing.T) {
	p := NewTavilyProvider("key", nil)
	require.Equal(t, "tavily", p.Name())
}

type tavilyRoundTripFunc func(*http.Request) (*http.Response, error)

func (f tavilyRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestTavilyProvider_Search_UsesBearerHeaderWithoutBodyKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
	}{
		{name: "configured key", key: "test-key"},
		{name: "pasted key with surrounding whitespace", key: " \ttest-key\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: tavilyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				require.Equal(t, http.MethodPost, req.Method)
				require.Equal(t, tavilySearchEndpoint, req.URL.String())
				require.Equal(t, "application/json", req.Header.Get("Content-Type"))
				require.Equal(t, "Bearer test-key", req.Header.Get("Authorization"))

				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				require.NotContains(t, string(body), "test-key")
				var payload map[string]any
				require.NoError(t, json.Unmarshal(body, &payload))
				require.Equal(t, map[string]any{
					"query": "golang", "max_results": float64(3), "search_depth": "basic",
				}, payload)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(strings.NewReader(`{"results":[{"url":"https://go.dev","title":"Go","content":"Go programming language"}]}`)),
					Header: make(http.Header),
				}, nil
			})}

			resp, err := NewTavilyProvider(tc.key, client).Search(context.Background(), SearchRequest{Query: "golang", MaxResults: 3})
			require.NoError(t, err)
			require.Equal(t, "golang", resp.Query)
			require.Equal(t, []SearchResult{{URL: "https://go.dev", Title: "Go", Snippet: "Go programming language"}}, resp.Results)
		})
	}
}

func TestTavilyProvider_Search_ResponseParsing(t *testing.T) {
	rawResp := `{"results":[{"url":"https://go.dev","title":"Go","content":"Go programming language","score":0.95}]}`
	var resp tavilyResponse
	require.NoError(t, json.Unmarshal([]byte(rawResp), &resp))
	require.Len(t, resp.Results, 1)
	require.Equal(t, "https://go.dev", resp.Results[0].URL)
	require.Equal(t, "Go programming language", resp.Results[0].Content)
	require.InDelta(t, 0.95, resp.Results[0].Score, 0.001)

	// Verify mapping to SearchResult
	results := make([]SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		results = append(results, SearchResult{
			URL: r.URL, Title: r.Title, Snippet: r.Content,
		})
	}
	require.Equal(t, "Go programming language", results[0].Snippet)
	require.Equal(t, "", results[0].PageAge)
}

func TestTavilyProvider_Search_EmptyResults(t *testing.T) {
	var resp tavilyResponse
	require.NoError(t, json.Unmarshal([]byte(`{"results":[]}`), &resp))
	require.Empty(t, resp.Results)
}

func TestTavilyProvider_Search_InvalidJSON(t *testing.T) {
	var resp tavilyResponse
	require.Error(t, json.Unmarshal([]byte("not json"), &resp))
}
