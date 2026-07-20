package repository

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type GitHubReleaseServiceSuite struct {
	suite.Suite
	srv     *httptest.Server
	client  *githubReleaseClient
	tempDir string
}

// testTransport redirects requests to the test server
type testTransport struct {
	testServerURL string
}

func (t *testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite the URL to point to our test server
	testURL := t.testServerURL + req.URL.Path
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, testURL, req.Body)
	if err != nil {
		return nil, err
	}
	newReq.Header = req.Header
	return http.DefaultTransport.RoundTrip(newReq)
}

func newTestGitHubReleaseClient() *githubReleaseClient {
	return &githubReleaseClient{
		httpClient:         &http.Client{},
		downloadHTTPClient: &http.Client{},
	}
}

func TestNewGitHubReleaseClientScopesUpdateTokenToAPIClient(t *testing.T) {
	t.Setenv("UPDATE_GITHUB_TOKEN", "update-secret")

	client := NewGitHubReleaseClient("", false)
	concrete, ok := client.(*githubReleaseClient)
	require.True(t, ok)
	require.Equal(t, "update-secret", concrete.updateGitHubToken)
	require.NotNil(t, concrete.httpClient)
	require.NotNil(t, concrete.downloadHTTPClient)
	require.NotSame(t, concrete.httpClient, concrete.downloadHTTPClient)
	require.NotNil(t, concrete.httpClient.CheckRedirect)
}

func TestNewGitHubReleaseClientProxyFailure(t *testing.T) {
	const invalidProxy = "http://[::1"

	client := NewGitHubReleaseClient(invalidProxy, false)
	errClient, ok := client.(*githubReleaseClientError)
	require.True(t, ok)
	require.Contains(t, errClient.err.Error(), "direct fallback is disabled")

	client = NewGitHubReleaseClient(invalidProxy, true)
	concrete, ok := client.(*githubReleaseClient)
	require.True(t, ok)
	require.NotNil(t, concrete.httpClient)
	require.NotNil(t, concrete.downloadHTTPClient)
}

func TestGitHubReleaseClientAPIRequestAuthorization(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantAuth string
	}{
		{name: "exact HTTPS authority", url: "https://api.github.com/repos/test/repo", wantAuth: "Bearer update-secret"},
		{name: "HTTP", url: "http://api.github.com/repos/test/repo"},
		{name: "subdomain", url: "https://sub.api.github.com/repos/test/repo"},
		{name: "userinfo", url: "https://user@api.github.com/repos/test/repo"},
		{name: "explicit default port", url: "https://api.github.com:443/repos/test/repo"},
		{name: "custom port", url: "https://api.github.com:8443/repos/test/repo"},
		{name: "different host", url: "https://github.com/test/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newTestGitHubReleaseClient()
			client.updateGitHubToken = "update-secret"
			req, err := client.newAPIRequest(context.Background(), tt.url)
			require.NoError(t, err)
			require.Equal(t, tt.wantAuth, req.Header.Get("Authorization"))
		})
	}

	client := newTestGitHubReleaseClient()
	req, err := client.newAPIRequest(context.Background(), "https://api.github.com/repos/test/repo")
	require.NoError(t, err)
	require.Empty(t, req.Header.Get("Authorization"))
}

func TestGitHubReleaseClientAPIRequestValidation(t *testing.T) {
	client := newTestGitHubReleaseClient()
	_, err := client.newAPIRequest(context.Background(), "://invalid-url")
	require.Error(t, err)
	require.False(t, isGitHubAPIURL(nil))
}

func TestGitHubReleaseClientRedirectAuthorization(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantAuth string
	}{
		{name: "same HTTPS authority", url: "https://api.github.com/redirected", wantAuth: "Bearer update-secret"},
		{name: "HTTP", url: "http://api.github.com/redirected"},
		{name: "subdomain", url: "https://sub.api.github.com/redirected"},
		{name: "userinfo", url: "https://user@api.github.com/redirected"},
		{name: "custom port", url: "https://api.github.com:8443/redirected"},
		{name: "different host", url: "https://example.com/redirected"},
	}

	checkRedirect := githubAPICheckRedirect(nil)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tt.url, nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer update-secret")

			require.NoError(t, checkRedirect(req, nil))
			require.Equal(t, tt.wantAuth, req.Header.Get("Authorization"))
		})
	}
}

func TestGitHubReleaseClientRedirectAuthorizationCallsPrevious(t *testing.T) {
	called := false
	checkRedirect := githubAPICheckRedirect(func(req *http.Request, via []*http.Request) error {
		called = true
		return context.Canceled
	})
	req, err := http.NewRequest(http.MethodGet, "https://example.com/redirected", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer update-secret")

	err = checkRedirect(req, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, called)
	require.Empty(t, req.Header.Get("Authorization"))
}

func TestGitHubReleaseClientDoesNotAuthorizeDownloads(t *testing.T) {
	client := newTestGitHubReleaseClient()
	client.updateGitHubToken = "update-secret"

	var headers []http.Header
	transport := githubReleaseRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		headers = append(headers, req.Header.Clone())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("checksum")),
			Request:    req,
		}, nil
	})
	client.httpClient.Transport = transport
	client.downloadHTTPClient.Transport = transport

	dest := filepath.Join(t.TempDir(), "asset")
	require.NoError(t, client.DownloadFile(context.Background(), "https://objects.githubusercontent.com/asset", dest, 100))
	_, err := client.FetchChecksumFile(context.Background(), "https://github.com/test/repo/releases/download/v1/checksums.txt")
	require.NoError(t, err)
	require.Len(t, headers, 2)
	for _, header := range headers {
		require.Empty(t, header.Get("Authorization"))
	}
}

type githubReleaseRoundTripFunc func(*http.Request) (*http.Response, error)

func (f githubReleaseRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func (s *GitHubReleaseServiceSuite) SetupTest() {
	s.tempDir = s.T().TempDir()
}

func (s *GitHubReleaseServiceSuite) TearDownTest() {
	if s.srv != nil {
		s.srv.Close()
		s.srv = nil
	}
}

func (s *GitHubReleaseServiceSuite) TestDownloadFile_EnforcesMaxSize_ContentLength() {
	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes.Repeat([]byte("a"), 100))
	}))

	s.client = newTestGitHubReleaseClient()

	dest := filepath.Join(s.tempDir, "file1.bin")
	err := s.client.DownloadFile(context.Background(), s.srv.URL, dest, 10)
	require.Error(s.T(), err, "expected error for oversized download with Content-Length")

	_, statErr := os.Stat(dest)
	require.Error(s.T(), statErr, "expected file to not exist for rejected download")
}

func (s *GitHubReleaseServiceSuite) TestDownloadFile_EnforcesMaxSize_Chunked() {
	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Force chunked encoding (unknown Content-Length) by flushing headers before writing.
		w.WriteHeader(http.StatusOK)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		for i := 0; i < 10; i++ {
			_, _ = w.Write(bytes.Repeat([]byte("b"), 10))
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		}
	}))

	s.client = newTestGitHubReleaseClient()

	dest := filepath.Join(s.tempDir, "file2.bin")
	err := s.client.DownloadFile(context.Background(), s.srv.URL, dest, 10)
	require.Error(s.T(), err, "expected error for oversized chunked download")

	_, statErr := os.Stat(dest)
	require.Error(s.T(), statErr, "expected file to be cleaned up for oversized chunked download")
}

func (s *GitHubReleaseServiceSuite) TestDownloadFile_Success() {
	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		for i := 0; i < 10; i++ {
			_, _ = w.Write(bytes.Repeat([]byte("b"), 10))
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		}
	}))

	s.client = newTestGitHubReleaseClient()

	dest := filepath.Join(s.tempDir, "file3.bin")
	err := s.client.DownloadFile(context.Background(), s.srv.URL, dest, 200)
	require.NoError(s.T(), err, "expected success")

	b, err := os.ReadFile(dest)
	require.NoError(s.T(), err, "read")
	require.True(s.T(), strings.HasPrefix(string(b), "b"), "downloaded content should start with 'b'")
	require.Len(s.T(), b, 100, "downloaded content length mismatch")
}

func (s *GitHubReleaseServiceSuite) TestDownloadFile_404() {
	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	s.client = newTestGitHubReleaseClient()

	dest := filepath.Join(s.tempDir, "notfound.bin")
	err := s.client.DownloadFile(context.Background(), s.srv.URL, dest, 100)
	require.Error(s.T(), err, "expected error for 404")

	_, statErr := os.Stat(dest)
	require.Error(s.T(), statErr, "expected file to not exist for 404")
}

func (s *GitHubReleaseServiceSuite) TestFetchChecksumFile_Success() {
	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("sum"))
	}))

	s.client = newTestGitHubReleaseClient()

	body, err := s.client.FetchChecksumFile(context.Background(), s.srv.URL)
	require.NoError(s.T(), err, "FetchChecksumFile")
	require.Equal(s.T(), "sum", string(body), "checksum body mismatch")
}

func (s *GitHubReleaseServiceSuite) TestFetchChecksumFile_Non200() {
	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))

	s.client = newTestGitHubReleaseClient()

	_, err := s.client.FetchChecksumFile(context.Background(), s.srv.URL)
	require.Error(s.T(), err, "expected error for non-200")
}

func (s *GitHubReleaseServiceSuite) TestDownloadFile_ContextCancel() {
	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))

	s.client = newTestGitHubReleaseClient()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dest := filepath.Join(s.tempDir, "cancelled.bin")
	err := s.client.DownloadFile(ctx, s.srv.URL, dest, 100)
	require.Error(s.T(), err, "expected error for cancelled context")
}

func (s *GitHubReleaseServiceSuite) TestDownloadFile_InvalidURL() {
	s.client = newTestGitHubReleaseClient()

	dest := filepath.Join(s.tempDir, "invalid.bin")
	err := s.client.DownloadFile(context.Background(), "://invalid-url", dest, 100)
	require.Error(s.T(), err, "expected error for invalid URL")
}

func (s *GitHubReleaseServiceSuite) TestDownloadFile_InvalidDestPath() {
	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("content"))
	}))

	s.client = newTestGitHubReleaseClient()

	// Use a path that cannot be created (directory doesn't exist)
	dest := filepath.Join(s.tempDir, "nonexistent", "subdir", "file.bin")
	err := s.client.DownloadFile(context.Background(), s.srv.URL, dest, 100)
	require.Error(s.T(), err, "expected error for invalid destination path")
}

func (s *GitHubReleaseServiceSuite) TestFetchChecksumFile_InvalidURL() {
	s.client = newTestGitHubReleaseClient()

	_, err := s.client.FetchChecksumFile(context.Background(), "://invalid-url")
	require.Error(s.T(), err, "expected error for invalid URL")
}

func (s *GitHubReleaseServiceSuite) TestFetchLatestRelease_Success() {
	releaseJSON := `{
		"tag_name": "v1.0.0",
		"name": "Release 1.0.0",
		"body": "Release notes",
		"html_url": "https://github.com/test/repo/releases/v1.0.0",
		"assets": [
			{
				"name": "app-linux-amd64.tar.gz",
				"browser_download_url": "https://github.com/test/repo/releases/download/v1.0.0/app-linux-amd64.tar.gz"
			}
		]
	}`

	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(s.T(), "/repos/test/repo/releases/latest", r.URL.Path)
		require.Equal(s.T(), "application/vnd.github.v3+json", r.Header.Get("Accept"))
		require.Equal(s.T(), "Sub2API-Updater", r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(releaseJSON))
	}))

	// Use custom transport to redirect requests to test server
	s.client = &githubReleaseClient{
		httpClient: &http.Client{
			Transport: &testTransport{testServerURL: s.srv.URL},
		},
		downloadHTTPClient: &http.Client{},
	}

	release, err := s.client.FetchLatestRelease(context.Background(), "test/repo")
	require.NoError(s.T(), err)
	require.Equal(s.T(), "v1.0.0", release.TagName)
	require.Equal(s.T(), "Release 1.0.0", release.Name)
	require.Len(s.T(), release.Assets, 1)
	require.Equal(s.T(), "app-linux-amd64.tar.gz", release.Assets[0].Name)
}

func (s *GitHubReleaseServiceSuite) TestFetchLatestRelease_Non200() {
	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	s.client = &githubReleaseClient{
		httpClient: &http.Client{
			Transport: &testTransport{testServerURL: s.srv.URL},
		},
		downloadHTTPClient: &http.Client{},
	}

	_, err := s.client.FetchLatestRelease(context.Background(), "test/repo")
	require.Error(s.T(), err)
	require.Contains(s.T(), err.Error(), "404")
}

func (s *GitHubReleaseServiceSuite) TestFetchLatestRelease_InvalidJSON() {
	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not valid json"))
	}))

	s.client = &githubReleaseClient{
		httpClient: &http.Client{
			Transport: &testTransport{testServerURL: s.srv.URL},
		},
		downloadHTTPClient: &http.Client{},
	}

	_, err := s.client.FetchLatestRelease(context.Background(), "test/repo")
	require.Error(s.T(), err)
}

func (s *GitHubReleaseServiceSuite) TestFetchLatestRelease_ContextCancel() {
	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))

	s.client = &githubReleaseClient{
		httpClient: &http.Client{
			Transport: &testTransport{testServerURL: s.srv.URL},
		},
		downloadHTTPClient: &http.Client{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.client.FetchLatestRelease(ctx, "test/repo")
	require.Error(s.T(), err)
}

func (s *GitHubReleaseServiceSuite) TestFetchChecksumFile_ContextCancel() {
	s.srv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))

	s.client = newTestGitHubReleaseClient()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.client.FetchChecksumFile(ctx, s.srv.URL)
	require.Error(s.T(), err)
}

func TestGitHubReleaseServiceSuite(t *testing.T) {
	suite.Run(t, new(GitHubReleaseServiceSuite))
}
