package service

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

const (
	OpenAIAuthModeAgentIdentity          = "agentIdentity"
	agentIdentityAuthAPIBaseURL          = "https://auth.openai.com/api/accounts"
	agentIdentityTaskRegistrationTimeout = 30 * time.Second
)

// Kept replaceable so the registration protocol can be tested without a real
// OpenAI account or network request.
var openAIAgentIdentityAuthAPIBaseURL = agentIdentityAuthAPIBaseURL

var agentIdentityTaskLocks sync.Map // map[int64]*sync.Mutex

type agentIdentityKey struct {
	runtimeID  string
	privateKey ed25519.PrivateKey
	taskID     string
}

type agentIdentityTaskRegistrationResponse struct {
	TaskID               string `json:"task_id"`
	TaskIDCamel          string `json:"taskId"`
	EncryptedTaskID      string `json:"encrypted_task_id"`
	EncryptedTaskIDCamel string `json:"encryptedTaskId"`
}

// IsOpenAIAgentIdentity identifies the upstream auth mode without requiring
// an access token. Agent Identity authenticates with an assertion instead.
func (a *Account) IsOpenAIAgentIdentity() bool {
	if a == nil || !a.IsOpenAIOAuth() {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(a.GetCredential("auth_mode")), OpenAIAuthModeAgentIdentity)
}

func agentIdentityPrivateKey(account *Account) (ed25519.PrivateKey, error) {
	if account == nil {
		return nil, errors.New("agent identity account is nil")
	}
	raw := strings.TrimSpace(account.GetCredential("agent_private_key"))
	if raw == "" {
		return nil, errors.New("agent identity private key is missing")
	}
	der, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("agent identity private key is not valid base64")
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, errors.New("agent identity private key is not valid PKCS#8")
	}
	privateKey, ok := key.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("agent identity private key is not Ed25519")
	}
	return privateKey, nil
}

// ValidateOpenAIAgentIdentityPrivateKey validates key material without
// returning or logging the private key itself.
func ValidateOpenAIAgentIdentityPrivateKey(encoded string) error {
	account := &Account{Credentials: map[string]any{"agent_private_key": encoded}}
	_, err := agentIdentityPrivateKey(account)
	return err
}

func agentIdentityKeyFromAccount(account *Account) (agentIdentityKey, error) {
	privateKey, err := agentIdentityPrivateKey(account)
	if err != nil {
		return agentIdentityKey{}, err
	}
	runtimeID := strings.TrimSpace(account.GetCredential("agent_runtime_id"))
	if err := validateAgentRuntimeID(runtimeID); err != nil {
		return agentIdentityKey{}, err
	}
	return agentIdentityKey{
		runtimeID:  runtimeID,
		privateKey: privateKey,
		taskID:     strings.TrimSpace(account.GetCredential("task_id")),
	}, nil
}

func validateAgentRuntimeID(runtimeID string) error {
	runtimeID = strings.TrimSpace(runtimeID)
	if runtimeID == "" {
		return errors.New("agent identity runtime id is missing")
	}
	if len(runtimeID) > 256 {
		return errors.New("agent identity runtime id is too long")
	}
	for _, r := range runtimeID {
		if r <= ' ' || strings.ContainsRune("/\\?#", r) {
			return errors.New("agent identity runtime id contains invalid characters")
		}
	}
	return nil
}

func buildAgentAssertion(key agentIdentityKey, now time.Time) (string, error) {
	if key.runtimeID == "" || key.taskID == "" {
		return "", errors.New("agent identity runtime or task id is missing")
	}
	timestamp := now.UTC().Format(time.RFC3339)
	payload := []byte(key.runtimeID + ":" + key.taskID + ":" + timestamp)
	signature, err := key.privateKey.Sign(nil, payload, crypto.Hash(0))
	if err != nil {
		return "", errors.New("failed to sign agent assertion")
	}
	envelope := map[string]string{
		"agent_runtime_id": key.runtimeID,
		"task_id":          key.taskID,
		"timestamp":        timestamp,
		"signature":        base64.StdEncoding.EncodeToString(signature),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", errors.New("failed to serialize agent assertion")
	}
	return "AgentAssertion " + base64.RawURLEncoding.EncodeToString(encoded), nil
}

func signAgentTaskRegistration(key agentIdentityKey, timestamp time.Time) (string, string, error) {
	if err := validateAgentRuntimeID(key.runtimeID); err != nil {
		return "", "", err
	}
	formatted := timestamp.UTC().Format(time.RFC3339)
	signature, err := key.privateKey.Sign(nil, []byte(key.runtimeID+":"+formatted), crypto.Hash(0))
	if err != nil {
		return "", "", errors.New("failed to sign agent task registration")
	}
	return formatted, base64.StdEncoding.EncodeToString(signature), nil
}

func decryptAgentTaskID(key agentIdentityKey, encoded string) (string, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", errors.New("encrypted agent task id is not valid base64")
	}
	seed := key.privateKey.Seed()
	digest := sha512.Sum512(seed)
	var curvePrivate [32]byte
	copy(curvePrivate[:], digest[:32])
	curvePrivate[0] &= 248
	curvePrivate[31] &= 127
	curvePrivate[31] |= 64
	curvePublicBytes, err := curve25519.X25519(curvePrivate[:], curve25519.Basepoint)
	if err != nil {
		return "", errors.New("failed to derive agent identity decryption key")
	}
	var curvePublic [32]byte
	copy(curvePublic[:], curvePublicBytes)
	plaintext, ok := box.OpenAnonymous(nil, ciphertext, &curvePublic, &curvePrivate)
	if !ok {
		return "", errors.New("failed to decrypt encrypted agent task id")
	}
	taskID := strings.TrimSpace(string(plaintext))
	if taskID == "" {
		return "", errors.New("decrypted agent task id is empty")
	}
	return taskID, nil
}

func registerAgentIdentityTask(ctx context.Context, account *Account) (string, error) {
	key, err := agentIdentityKeyFromAccount(account)
	if err != nil {
		return "", err
	}
	timestamp, signature, err := signAgentTaskRegistration(key, time.Now())
	if err != nil {
		return "", err
	}
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	client, err := httpclient.GetClient(httpclient.Options{
		ProxyURL:              proxyURL,
		Timeout:               agentIdentityTaskRegistrationTimeout,
		ResponseHeaderTimeout: 15 * time.Second,
	})
	if err != nil {
		return "", errors.New("invalid proxy configuration for agent task registration")
	}
	body, err := json.Marshal(map[string]string{
		"timestamp": timestamp,
		"signature": signature,
	})
	if err != nil {
		return "", errors.New("failed to serialize agent task registration")
	}
	base := strings.TrimRight(strings.TrimSpace(openAIAgentIdentityAuthAPIBaseURL), "/")
	endpoint := base + "/v1/agent/" + url.PathEscape(key.runtimeID) + "/task/register"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", errors.New("failed to build agent task registration request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("agent task registration request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("agent task registration returned status %d", resp.StatusCode)
	}
	var result agentIdentityTaskRegistrationResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&result); err != nil {
		return "", errors.New("agent task registration response is invalid")
	}
	if taskID := strings.TrimSpace(result.TaskID); taskID != "" {
		return taskID, nil
	}
	if taskID := strings.TrimSpace(result.TaskIDCamel); taskID != "" {
		return taskID, nil
	}
	encrypted := strings.TrimSpace(result.EncryptedTaskID)
	if encrypted == "" {
		encrypted = strings.TrimSpace(result.EncryptedTaskIDCamel)
	}
	if encrypted == "" {
		return "", errors.New("agent task registration response omitted task id")
	}
	return decryptAgentTaskID(key, encrypted)
}

// ensureAgentIdentityTaskForAccount registers a task only when the current
// account snapshot does not already have one. The repository re-read inside
// the lock closes the duplicate-registration race between stale snapshots.
func ensureAgentIdentityTaskForAccount(ctx context.Context, repo AccountRepository, account *Account, expectedTaskID string) error {
	if account == nil || !account.IsOpenAIAgentIdentity() {
		return nil
	}
	if current := strings.TrimSpace(account.GetCredential("task_id")); current != "" && (expectedTaskID == "" || current != expectedTaskID) {
		return nil
	}
	lock := accountIdentityTaskLock(account.ID)
	lock.Lock()
	defer lock.Unlock()

	credAccount := account
	if repo != nil && account.ID > 0 {
		if refreshed, refreshErr := repo.GetByID(ctx, account.ID); refreshErr == nil && refreshed != nil {
			credAccount = refreshed
		}
	}
	if !credAccount.IsOpenAIAgentIdentity() {
		return errors.New("agent identity credentials are unavailable")
	}
	current := strings.TrimSpace(credAccount.GetCredential("task_id"))
	if current != "" && (expectedTaskID == "" || current != expectedTaskID) {
		account.Credentials = cloneCredentials(credAccount.Credentials)
		return nil
	}
	newTaskID, err := registerAgentIdentityTask(ctx, credAccount)
	if err != nil {
		return err
	}
	credentials := cloneCredentials(credAccount.Credentials)
	credentials["task_id"] = newTaskID
	if err := persistAccountCredentials(ctx, repo, credAccount, credentials); err != nil {
		return err
	}
	if account != credAccount {
		account.Credentials = cloneCredentials(credentials)
	}
	return nil
}

// EnsureOpenAIAgentIdentityTask makes sure an Agent Identity account has a
// current upstream task. Gateway and quota integrations use this entry point
// when they add task-invalid recovery in follow-up phases.
func EnsureOpenAIAgentIdentityTask(ctx context.Context, repo AccountRepository, account *Account, expectedTaskID string) error {
	return ensureAgentIdentityTaskForAccount(ctx, repo, account, expectedTaskID)
}

func accountIdentityTaskLock(accountID int64) *sync.Mutex {
	if accountID <= 0 {
		return &sync.Mutex{}
	}
	lock := &sync.Mutex{}
	actual, _ := agentIdentityTaskLocks.LoadOrStore(accountID, lock)
	if shared, ok := actual.(*sync.Mutex); ok {
		return shared
	}
	return lock
}

func buildAgentIdentityAuthenticationHeaders(ctx context.Context, repo AccountRepository, account *Account) (http.Header, error) {
	if account == nil || !account.IsOpenAIAgentIdentity() {
		return nil, errors.New("agent identity account is required")
	}
	if err := ensureAgentIdentityTaskForAccount(ctx, repo, account, ""); err != nil {
		return nil, err
	}
	key, err := agentIdentityKeyFromAccount(account)
	if err != nil {
		return nil, err
	}
	assertion, err := buildAgentAssertion(key, time.Now())
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set("Authorization", assertion)
	return headers, nil
}

// BuildOpenAIAgentIdentityAuthenticationHeaders returns the assertion header
// required by Agent Identity upstream requests.
func BuildOpenAIAgentIdentityAuthenticationHeaders(ctx context.Context, repo AccountRepository, account *Account) (http.Header, error) {
	return buildAgentIdentityAuthenticationHeaders(ctx, repo, account)
}
