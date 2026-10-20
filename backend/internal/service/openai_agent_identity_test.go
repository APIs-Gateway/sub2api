//go:build unit

package service

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

func newAgentIdentityTestCredentials(t *testing.T) (ed25519.PublicKey, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(cryptorand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	return publicKey, base64.StdEncoding.EncodeToString(der)
}

func newAgentIdentityTestAccount(t *testing.T, privateKey string) *Account {
	t.Helper()
	return &Account{
		ID:       42,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":         OpenAIAuthModeAgentIdentity,
			"agent_runtime_id":  "runtime-test-1",
			"agent_private_key": privateKey,
		},
	}
}

func encryptedAgentTaskIDForTest(t *testing.T, key agentIdentityKey, taskID string) string {
	t.Helper()
	digest := sha512ForTest(key.privateKey.Seed())
	var privateCurve [32]byte
	copy(privateCurve[:], digest[:32])
	privateCurve[0] &= 248
	privateCurve[31] &= 127
	privateCurve[31] |= 64
	publicBytes, err := curve25519.X25519(privateCurve[:], curve25519.Basepoint)
	require.NoError(t, err)
	var publicCurve [32]byte
	copy(publicCurve[:], publicBytes)
	ciphertext, err := box.SealAnonymous(nil, []byte(taskID), &publicCurve, cryptorand.Reader)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(ciphertext)
}

func TestValidateOpenAIAgentIdentityPrivateKey(t *testing.T) {
	_, encoded := newAgentIdentityTestCredentials(t)
	require.NoError(t, ValidateOpenAIAgentIdentityPrivateKey(encoded))
	require.Error(t, ValidateOpenAIAgentIdentityPrivateKey("not-base64"))
	require.Error(t, ValidateOpenAIAgentIdentityPrivateKey(base64.StdEncoding.EncodeToString([]byte("not-a-key"))))
	rsaKey, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	require.NoError(t, err)
	rsaDER, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	require.NoError(t, err)
	require.Error(t, ValidateOpenAIAgentIdentityPrivateKey(base64.StdEncoding.EncodeToString(rsaDER)))
}

func TestIsOpenAIAgentIdentityRequiresOpenAIOAuthMode(t *testing.T) {
	_, encoded := newAgentIdentityTestCredentials(t)
	cases := []struct {
		name     string
		platform string
		kind     string
		mode     string
		want     bool
	}{
		{name: "agent identity", platform: PlatformOpenAI, kind: AccountTypeOAuth, mode: OpenAIAuthModeAgentIdentity, want: true},
		{name: "case insensitive mode", platform: PlatformOpenAI, kind: AccountTypeOAuth, mode: "AGENTIDENTITY", want: true},
		{name: "wrong platform", platform: PlatformAnthropic, kind: AccountTypeOAuth, mode: OpenAIAuthModeAgentIdentity},
		{name: "wrong account type", platform: PlatformOpenAI, kind: AccountTypeAPIKey, mode: OpenAIAuthModeAgentIdentity},
		{name: "wrong auth mode", platform: PlatformOpenAI, kind: AccountTypeOAuth, mode: "oauth"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{
				Platform: tc.platform,
				Type:     tc.kind,
				Credentials: map[string]any{
					"auth_mode":         tc.mode,
					"agent_private_key": encoded,
				},
			}
			require.Equal(t, tc.want, account.IsOpenAIAgentIdentity())
		})
	}
	require.False(t, (*Account)(nil).IsOpenAIAgentIdentity())
}

func TestAgentIdentityKeyAndAssertionRejectMissingFields(t *testing.T) {
	_, encoded := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, encoded)
	account.Credentials["agent_runtime_id"] = "bad/runtime"
	_, err := agentIdentityKeyFromAccount(account)
	require.EqualError(t, err, "agent identity runtime id contains invalid characters")

	account = newAgentIdentityTestAccount(t, encoded)
	key, err := agentIdentityKeyFromAccount(account)
	require.NoError(t, err)
	_, err = buildAgentAssertion(key, time.Now())
	require.EqualError(t, err, "agent identity runtime or task id is missing")
	_, _, err = signAgentTaskRegistration(agentIdentityKey{runtimeID: "", privateKey: key.privateKey}, time.Now())
	require.EqualError(t, err, "agent identity runtime id is missing")
	_, err = agentIdentityKeyFromAccount(nil)
	require.EqualError(t, err, "agent identity account is nil")
}

func TestBuildAgentAssertion(t *testing.T) {
	publicKey, encoded := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, encoded)
	key, err := agentIdentityKeyFromAccount(account)
	require.NoError(t, err)
	key.taskID = "task-test-1"
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	assertion, err := buildAgentAssertion(key, now)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(assertion, "AgentAssertion "))

	encodedPayload := strings.TrimPrefix(assertion, "AgentAssertion ")
	payload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	require.NoError(t, err)
	var envelope map[string]string
	require.NoError(t, json.Unmarshal(payload, &envelope))
	require.Equal(t, key.runtimeID, envelope["agent_runtime_id"])
	require.Equal(t, key.taskID, envelope["task_id"])
	require.Equal(t, now.Format(time.RFC3339), envelope["timestamp"])
	signature, err := base64.StdEncoding.DecodeString(envelope["signature"])
	require.NoError(t, err)
	require.True(t, ed25519.Verify(publicKey, []byte(key.runtimeID+":"+key.taskID+":"+now.Format(time.RFC3339)), signature))
}

func TestDecryptAgentTaskID(t *testing.T) {
	_, encoded := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, encoded)
	key, err := agentIdentityKeyFromAccount(account)
	require.NoError(t, err)

	encodedTaskID := encryptedAgentTaskIDForTest(t, key, "encrypted-task")
	decoded, err := decryptAgentTaskID(key, encodedTaskID)
	require.NoError(t, err)
	require.Equal(t, "encrypted-task", decoded)
	_, err = decryptAgentTaskID(key, "not-base64")
	require.EqualError(t, err, "encrypted agent task id is not valid base64")
	_, err = decryptAgentTaskID(key, base64.StdEncoding.EncodeToString([]byte("invalid-box")))
	require.EqualError(t, err, "failed to decrypt encrypted agent task id")
}

func TestRegisterAgentIdentityTaskSupportsTaskIDVariants(t *testing.T) {
	_, encoded := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, encoded)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/agent/runtime-test-1/task/register" {
			t.Errorf("path = %s, want /v1/agent/runtime-test-1/task/register", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type = %s, want application/json", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		var request map[string]string
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode request body: %v", err)
			return
		}
		if request["timestamp"] == "" || request["signature"] == "" {
			t.Errorf("registration request omitted signed fields: %#v", request)
			return
		}
		_, _ = io.WriteString(w, `{"taskId":"task-from-server"}`)
	}))
	defer server.Close()

	original := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = server.URL
	defer func() { openAIAgentIdentityAuthAPIBaseURL = original }()

	taskID, err := registerAgentIdentityTask(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "task-from-server", taskID)
}

func TestRegisterAgentIdentityTaskSupportsEncryptedTaskID(t *testing.T) {
	_, encoded := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, encoded)
	key, err := agentIdentityKeyFromAccount(account)
	require.NoError(t, err)
	encrypted := encryptedAgentTaskIDForTest(t, key, "task-encrypted-server")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"encrypted_task_id":%q}`, encrypted)
	}))
	defer server.Close()

	original := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = server.URL
	defer func() { openAIAgentIdentityAuthAPIBaseURL = original }()

	taskID, err := registerAgentIdentityTask(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "task-encrypted-server", taskID)
}

func TestRegisterAgentIdentityTaskRejectsInvalidResponses(t *testing.T) {
	_, encoded := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, encoded)
	responses := []struct {
		name       string
		statusCode int
		body       string
		want       string
	}{
		{name: "upstream status", statusCode: http.StatusBadGateway, body: "upstream failure", want: "agent task registration returned status 502"},
		{name: "invalid json", statusCode: http.StatusOK, body: "not-json", want: "agent task registration response is invalid"},
		{name: "missing task", statusCode: http.StatusOK, body: `{}`, want: "agent task registration response omitted task id"},
		{name: "invalid encrypted task", statusCode: http.StatusOK, body: `{"encrypted_task_id":"bad"}`, want: "encrypted agent task id is not valid base64"},
	}
	for _, tc := range responses {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			original := openAIAgentIdentityAuthAPIBaseURL
			openAIAgentIdentityAuthAPIBaseURL = server.URL
			defer func() { openAIAgentIdentityAuthAPIBaseURL = original }()

			_, err := registerAgentIdentityTask(context.Background(), account)
			require.EqualError(t, err, tc.want)
		})
	}

	badRuntime := newAgentIdentityTestAccount(t, encoded)
	badRuntime.Credentials["agent_runtime_id"] = "runtime/invalid"
	_, err := registerAgentIdentityTask(context.Background(), badRuntime)
	require.EqualError(t, err, "agent identity runtime id contains invalid characters")

	badProxy := newAgentIdentityTestAccount(t, encoded)
	badProxy.ProxyID = ptrInt64ForAgentIdentityTest(1)
	badProxy.Proxy = &Proxy{Protocol: "unsupported", Host: "localhost", Port: 1}
	_, err = registerAgentIdentityTask(context.Background(), badProxy)
	require.Error(t, err)
}

type agentIdentityRepoStub struct {
	mockAccountRepoForGemini
	mu          sync.Mutex
	updateCalls int
	updateErr   error
}

func (r *agentIdentityRepoStub) UpdateCredentials(_ context.Context, id int64, credentials map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateCalls++
	if r.updateErr != nil {
		return r.updateErr
	}
	if account := r.accountsByID[id]; account != nil {
		account.Credentials = cloneCredentials(credentials)
	}
	return nil
}

func ptrInt64ForAgentIdentityTest(value int64) *int64 {
	return &value
}

func TestEnsureAgentIdentityTaskRegistersOnce(t *testing.T) {
	_, encoded := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, encoded)
	repo := &agentIdentityRepoStub{
		mockAccountRepoForGemini: mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: account},
		},
	}
	var registrations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registrations.Add(1)
		time.Sleep(20 * time.Millisecond)
		_, _ = io.WriteString(w, `{"task_id":"task-once"}`)
	}))
	defer server.Close()

	original := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = server.URL
	defer func() { openAIAgentIdentityAuthAPIBaseURL = original }()

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- ensureAgentIdentityTaskForAccount(context.Background(), repo, account, "")
		}()
	}
	wg.Wait()
	for i := 0; i < 2; i++ {
		require.NoError(t, <-errs)
	}

	require.Equal(t, int32(1), registrations.Load())
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, "task-once", account.GetCredential("task_id"))
}

func TestEnsureAgentIdentityTaskNoOpsForNonAgentOrExistingTask(t *testing.T) {
	_, encoded := newAgentIdentityTestCredentials(t)
	normal := &Account{
		ID:       77,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":    "oauth",
			"access_token": "token",
		},
	}
	require.NoError(t, EnsureOpenAIAgentIdentityTask(context.Background(), nil, normal, ""))
	require.NoError(t, EnsureOpenAIAgentIdentityTask(context.Background(), nil, nil, ""))

	account := newAgentIdentityTestAccount(t, encoded)
	account.Credentials["task_id"] = "already-current"
	require.NoError(t, EnsureOpenAIAgentIdentityTask(context.Background(), nil, account, ""))
	require.Equal(t, "already-current", account.GetCredential("task_id"))
}

func TestEnsureAgentIdentityTaskRefreshesRepositorySnapshot(t *testing.T) {
	_, encoded := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, encoded)
	stored := newAgentIdentityTestAccount(t, encoded)
	stored.Credentials["task_id"] = "task-from-repository"
	repo := &agentIdentityRepoStub{
		mockAccountRepoForGemini: mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: stored},
		},
	}
	require.NoError(t, EnsureOpenAIAgentIdentityTask(context.Background(), repo, account, ""))
	require.Equal(t, "task-from-repository", account.GetCredential("task_id"))
	require.Equal(t, 0, repo.updateCalls)
}

func TestEnsureAgentIdentityTaskReturnsPersistenceError(t *testing.T) {
	_, encoded := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, encoded)
	repo := &agentIdentityRepoStub{
		mockAccountRepoForGemini: mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: account},
		},
		updateErr: errors.New("persist failed"),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"task_id":"task-not-persisted"}`)
	}))
	defer server.Close()
	original := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = server.URL
	defer func() { openAIAgentIdentityAuthAPIBaseURL = original }()

	err := EnsureOpenAIAgentIdentityTask(context.Background(), repo, account, "")
	require.EqualError(t, err, "persist failed")
}

func TestBuildAgentIdentityAuthenticationHeadersUsesTask(t *testing.T) {
	_, encoded := newAgentIdentityTestCredentials(t)
	account := newAgentIdentityTestAccount(t, encoded)
	account.Credentials["task_id"] = "task-header"
	headers, err := BuildOpenAIAgentIdentityAuthenticationHeaders(context.Background(), nil, account)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(headers.Get("Authorization"), "AgentAssertion "))
	_, err = BuildOpenAIAgentIdentityAuthenticationHeaders(context.Background(), nil, &Account{})
	require.EqualError(t, err, "agent identity account is required")
}

func sha512ForTest(input []byte) [64]byte {
	return sha512.Sum512(input)
}
