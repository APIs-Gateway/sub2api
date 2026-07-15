//go:build unit

package service

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
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

func TestValidateOpenAIAgentIdentityPrivateKey(t *testing.T) {
	_, encoded := newAgentIdentityTestCredentials(t)
	require.NoError(t, ValidateOpenAIAgentIdentityPrivateKey(encoded))
	require.Error(t, ValidateOpenAIAgentIdentityPrivateKey("not-base64"))
	require.Error(t, ValidateOpenAIAgentIdentityPrivateKey(base64.StdEncoding.EncodeToString([]byte("not-a-key"))))
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
	ciphertext, err := box.SealAnonymous(nil, []byte("encrypted-task"), &publicCurve, cryptorand.Reader)
	require.NoError(t, err)
	decoded, err := decryptAgentTaskID(key, base64.StdEncoding.EncodeToString(ciphertext))
	require.NoError(t, err)
	require.Equal(t, "encrypted-task", decoded)
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

type agentIdentityRepoStub struct {
	mockAccountRepoForGemini
	mu          sync.Mutex
	updateCalls int
}

func (r *agentIdentityRepoStub) UpdateCredentials(_ context.Context, id int64, credentials map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateCalls++
	if account := r.accountsByID[id]; account != nil {
		account.Credentials = cloneCredentials(credentials)
	}
	return nil
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

func sha512ForTest(input []byte) [64]byte {
	return sha512.Sum512(input)
}
