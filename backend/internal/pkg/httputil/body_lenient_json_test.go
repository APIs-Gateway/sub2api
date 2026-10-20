package httputil

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func (failingReader) Close() error {
	return nil
}

func TestNormalizeLenientJSONRequestBody_accepts_client_control_chars_in_strings(t *testing.T) {
	tests := []struct {
		name    string
		body    []byte
		path    string
		want    string
		wantRaw string
	}{
		{
			name:    "null byte in message content",
			body:    []byte("{\"messages\":[{\"content\":\"hello\x00world\"}]}"),
			path:    "messages.0.content",
			want:    "hello\x00world",
			wantRaw: `"hello\u0000world"`,
		},
		{
			name:    "ansi escape in message content",
			body:    []byte("{\"messages\":[{\"content\":\"hello\x1b[31mred\x1b[0m\"}]}"),
			path:    "messages.0.content",
			want:    "hello\x1b[31mred\x1b[0m",
			wantRaw: `"hello\u001b[31mred\u001b[0m"`,
		},
		{
			name:    "leading UTF-8 BOM",
			body:    []byte("\xef\xbb\xbf{\"input\":\"hello\"}"),
			path:    "input",
			want:    "hello",
			wantRaw: `"hello"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			if gjson.ValidBytes(tt.body) {
				t.Fatalf("test payload should reproduce strict JSON rejection: %q", tt.body)
			}

			// When
			got, err := NormalizeLenientJSONRequestBody(tt.body, 1024)
			if err != nil {
				t.Fatalf("NormalizeLenientJSONRequestBody: %v", err)
			}

			// Then
			if !gjson.ValidBytes(got) {
				t.Fatalf("normalized body should be valid JSON: %q", got)
			}
			result := gjson.GetBytes(got, tt.path)
			if result.String() != tt.want {
				t.Fatalf("value mismatch: got %q want %q", result.String(), tt.want)
			}
			if result.Raw != tt.wantRaw {
				t.Fatalf("raw value mismatch: got %q want %q", result.Raw, tt.wantRaw)
			}
		})
	}
}

func TestNormalizeLenientJSONRequestBody_keeps_invalid_structure_invalid(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{
			name: "truncated JSON",
			body: []byte("{\"messages\":[{\"content\":\"hello\"}]"),
		},
		{
			name: "control character outside string",
			body: []byte("{\"input\":\"hello\"}\x00"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			got, err := NormalizeLenientJSONRequestBody(tt.body, 1024)
			if err != nil {
				t.Fatalf("NormalizeLenientJSONRequestBody: %v", err)
			}

			// Then
			if gjson.ValidBytes(got) {
				t.Fatalf("normalization must not repair invalid JSON structure: %q", got)
			}
		})
	}
}

func TestNormalizeLenientJSONRequestBody_allows_http_requests_with_client_control_chars(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Given
		body, err := ReadLenientJSONRequestBodyWithPrealloc(r, 1024)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// When
		if !gjson.ValidBytes(body) {
			http.Error(w, "Failed to parse request body", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	tests := []struct {
		name string
		body []byte
		want int
	}{
		{
			name: "null byte in JSON string",
			body: []byte("{\"model\":\"gpt-5.5\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\x00world\"}]}"),
			want: http.StatusAccepted,
		},
		{
			name: "ANSI escape in JSON string",
			body: []byte("{\"model\":\"gpt-5.5\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\x1b[31mred\x1b[0m\"}]}"),
			want: http.StatusAccepted,
		},
		{
			name: "leading UTF-8 BOM",
			body: []byte("\xef\xbb\xbf{\"model\":\"gpt-5.5\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}"),
			want: http.StatusAccepted,
		},
		{
			name: "truncated JSON",
			body: []byte("{\"model\":\"gpt-5.5\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]"),
			want: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", bytes.NewReader(tt.body))
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != tt.want {
				t.Fatalf("status mismatch: got %d want %d", resp.StatusCode, tt.want)
			}
		})
	}
}

func TestNormalizeLenientJSONRequestBody_rejects_expansion_past_limit(t *testing.T) {
	// Given
	body := []byte("{\"input\":\"\x00\x00\"}")

	// When
	_, err := NormalizeLenientJSONRequestBody(body, int64(len(body)+5))

	// Then
	var maxErr *http.MaxBytesError
	if !errors.As(err, &maxErr) {
		t.Fatalf("expected MaxBytesError, got %T %v", err, err)
	}
	if maxErr.Limit != int64(len(body)+5) {
		t.Fatalf("limit mismatch: got %d want %d", maxErr.Limit, len(body)+5)
	}
}

func TestNormalizeLenientJSONRequestBody_boundaryBranches(t *testing.T) {
	tests := []struct {
		name      string
		body      []byte
		limit     int64
		want      []byte
		wantLimit int64
	}{
		{
			name:  "default limit with empty body",
			body:  nil,
			limit: 0,
			want:  nil,
		},
		{
			name:  "trims UTF-8 BOM",
			body:  []byte("\xef\xbb\xbf{\"input\":\"ok\"}"),
			limit: 0,
			want:  []byte("{\"input\":\"ok\"}"),
		},
		{
			name:      "raw body exceeds configured limit",
			body:      []byte("{\"input\":\"too long\"}"),
			limit:     4,
			wantLimit: 4,
		},
		{
			name:      "trailing append after escape exceeds limit",
			body:      []byte("{\"input\":\"\x00a\"}"),
			limit:     16,
			wantLimit: 16,
		},
		{
			name:  "escaped quote keeps string state",
			body:  []byte("{\"input\":\"escaped \\\" quote \x7f\"}"),
			limit: 128,
			want:  []byte("{\"input\":\"escaped \\\" quote \\u007f\"}"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeLenientJSONRequestBody(tt.body, tt.limit)
			if tt.wantLimit > 0 {
				var maxErr *http.MaxBytesError
				if !errors.As(err, &maxErr) {
					t.Fatalf("expected MaxBytesError, got %T %v", err, err)
				}
				if maxErr.Limit != tt.wantLimit {
					t.Fatalf("limit mismatch: got %d want %d", maxErr.Limit, tt.wantLimit)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeLenientJSONRequestBody: %v", err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("body mismatch: got %q want %q", got, tt.want)
			}
		})
	}
}

func TestReadLenientJSONRequestBodyWithPrealloc_propagatesReadErrors(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Body = failingReader{}

	_, err := ReadLenientJSONRequestBodyWithPrealloc(req, 1024)
	if err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("expected read failure, got %v", err)
	}
}

func TestReadRequestBodyWithPrealloc_contentLengthCapacityBranches(t *testing.T) {
	tests := []struct {
		name          string
		contentLength int64
		body          string
	}{
		{
			name:          "small positive content length keeps minimum capacity",
			contentLength: 8,
			body:          "small",
		},
		{
			name:          "large content length caps initial capacity",
			contentLength: int64(requestBodyReadMaxInitCap) + 1,
			body:          "large",
		},
		{
			name:          "exact content length uses content length",
			contentLength: 1024,
			body:          strings.Repeat("x", 1024),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", io.NopCloser(strings.NewReader(tt.body)))
			req.ContentLength = tt.contentLength

			got, err := ReadRequestBodyWithPrealloc(req)
			if err != nil {
				t.Fatalf("ReadRequestBodyWithPrealloc: %v", err)
			}
			if string(got) != tt.body {
				t.Fatalf("body mismatch: got %q want %q", got, tt.body)
			}
		})
	}
}
