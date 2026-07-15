package service

import (
	"bufio"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitOpenAIConcatenatedJSONDocuments(t *testing.T) {
	first := `{"type":"response.created","id":"resp_1"}`
	second := `{"type":"response.completed","id":"resp_1"}`

	documents, repaired := splitOpenAIConcatenatedJSONDocuments([]byte(first + second))
	require.True(t, repaired)
	require.Equal(t, [][]byte{[]byte(first), []byte(second)}, documents)

	documents, repaired = splitOpenAIConcatenatedJSONDocuments([]byte(first))
	require.False(t, repaired)
	require.Nil(t, documents)
}

func TestSplitOpenAIConcatenatedJSONDocumentsRejectsAmbiguousPayloads(t *testing.T) {
	cases := []string{
		`{"id":"missing_type"}{"type":"response.completed"}`,
		`{"type":"response.created"}not-json`,
		`{"type":"response.created\ninvalid"}{"type":"response.completed"}`,
	}
	for _, payload := range cases {
		documents, repaired := splitOpenAIConcatenatedJSONDocuments([]byte(payload))
		require.False(t, repaired, payload)
		require.Nil(t, documents, payload)
	}

	tooMany := make([]string, 0, maxOpenAIConcatenatedJSONDocuments+1)
	for i := 0; i < maxOpenAIConcatenatedJSONDocuments+1; i++ {
		tooMany = append(tooMany, `{"type":"response.output_text.delta"}`)
	}
	documents, repaired := splitOpenAIConcatenatedJSONDocuments([]byte(strings.Join(tooMany, "")))
	require.False(t, repaired)
	require.Nil(t, documents)
}

func TestOpenAISSEJSONDocumentScannerExpandsConcatenatedDataLine(t *testing.T) {
	first := `{"type":"response.created","id":"resp_1"}`
	second := `{"type":"response.completed","id":"resp_1"}`
	scanner := newOpenAISSEJSONDocumentScanner(bufio.NewScanner(strings.NewReader("data: " + first + second + "\n")))

	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	require.Equal(t, []string{
		"data: " + first,
		"",
		"event: response.completed",
		"data: " + second,
		"",
	}, lines)
	require.NoError(t, scanner.Err())
}

func TestOpenAISSEJSONDocumentScannerPreservesNormalLines(t *testing.T) {
	scanner := newOpenAISSEJSONDocumentScanner(bufio.NewScanner(strings.NewReader("event: response.created\n\nnot-data\n")))

	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	require.Equal(t, []string{"event: response.created", "", "not-data"}, lines)
	require.NoError(t, scanner.Err())
	require.False(t, newOpenAISSEJSONDocumentScanner(nil).Scan())
	require.NoError(t, newOpenAISSEJSONDocumentScanner(nil).Err())
}
