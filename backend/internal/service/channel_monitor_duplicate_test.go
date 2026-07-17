//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

type duplicateChannelMonitorRepoStub struct {
	ChannelMonitorRepository
	source      *ChannelMonitor
	created     []*ChannelMonitor
	byOperation map[string]*ChannelMonitor
	nextID      int64
	createErr   error
	findErr     error
}

func (r *duplicateChannelMonitorRepoStub) GetByID(_ context.Context, id int64) (*ChannelMonitor, error) {
	if r.source == nil || r.source.ID != id {
		return nil, ErrChannelMonitorNotFound
	}
	return r.source, nil
}

func (r *duplicateChannelMonitorRepoStub) Create(_ context.Context, monitor *ChannelMonitor) error {
	if r.createErr != nil {
		return r.createErr
	}
	r.nextID++
	monitor.ID = 100 + r.nextID
	monitor.CreatedAt = time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)
	monitor.UpdatedAt = monitor.CreatedAt

	stored := *monitor
	stored.ExtraModels = append([]string(nil), monitor.ExtraModels...)
	stored.ExtraHeaders = cloneStringMap(monitor.ExtraHeaders)
	stored.BodyOverride = mustCloneJSONMap(monitor.BodyOverride)
	if monitor.TemplateID != nil {
		templateID := *monitor.TemplateID
		stored.TemplateID = &templateID
	}
	r.created = append(r.created, &stored)
	if stored.DuplicateOperationID != "" {
		if r.byOperation == nil {
			r.byOperation = make(map[string]*ChannelMonitor)
		}
		r.byOperation[stored.DuplicateOperationID] = &stored
	}
	return nil
}

func (r *duplicateChannelMonitorRepoStub) FindByDuplicateOperationID(_ context.Context, operationID string) (*ChannelMonitor, error) {
	if r.findErr != nil {
		return nil, r.findErr
	}
	monitor := r.byOperation[operationID]
	if monitor == nil {
		return nil, nil
	}
	cloned := *monitor
	cloned.ExtraModels = append([]string(nil), monitor.ExtraModels...)
	cloned.ExtraHeaders = cloneStringMap(monitor.ExtraHeaders)
	cloned.BodyOverride = mustCloneJSONMap(monitor.BodyOverride)
	return &cloned, nil
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func mustCloneJSONMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	cloned, err := cloneChannelMonitorJSONMap(source)
	if err != nil {
		panic(err)
	}
	return cloned
}

type duplicateChannelMonitorEncryptor struct {
	decryptErr error
	encryptErr error
}

func (e *duplicateChannelMonitorEncryptor) Encrypt(plaintext string) (string, error) {
	if e.encryptErr != nil {
		return "", e.encryptErr
	}
	return "NEW:" + plaintext, nil
}

func (e *duplicateChannelMonitorEncryptor) Decrypt(ciphertext string) (string, error) {
	if e.decryptErr != nil {
		return "", e.decryptErr
	}
	if !strings.HasPrefix(ciphertext, "OLD:") && !strings.HasPrefix(ciphertext, "NEW:") {
		return "", errors.New("invalid ciphertext")
	}
	return strings.TrimPrefix(strings.TrimPrefix(ciphertext, "OLD:"), "NEW:"), nil
}

func TestDuplicateChannelMonitorCopiesConfigurationAndResetsRuntimeState(t *testing.T) {
	lastCheckedAt := time.Date(2026, time.July, 15, 7, 0, 0, 0, time.UTC)
	templateID := int64(9)
	source := &ChannelMonitor{
		ID:               42,
		Name:             "primary",
		Provider:         MonitorProviderOpenAI,
		APIMode:          MonitorAPIModeResponses,
		Endpoint:         "https://api.example.com",
		APIKey:           "OLD:top-secret",
		PrimaryModel:     "gpt-5.4-mini",
		ExtraModels:      []string{"gpt-5.4", "gpt-5.3"},
		GroupName:        "production",
		Enabled:          true,
		IntervalSeconds:  90,
		JitterSeconds:    15,
		LastCheckedAt:    &lastCheckedAt,
		CreatedBy:        4,
		CreatedAt:        lastCheckedAt.Add(-time.Hour),
		UpdatedAt:        lastCheckedAt,
		TemplateID:       &templateID,
		ExtraHeaders:     map[string]string{"User-Agent": "Codex"},
		BodyOverrideMode: MonitorBodyOverrideModeMerge,
		BodyOverride: map[string]any{
			"metadata": map[string]any{"source": "original"},
		},
	}
	repo := &duplicateChannelMonitorRepoStub{source: source}
	service := NewChannelMonitorService(repo, &duplicateChannelMonitorEncryptor{})

	duplicate, err := service.Duplicate(context.Background(), source.ID, 77, "admin:77", "copy-primary")

	require.NoError(t, err)
	require.Len(t, repo.created, 1)
	stored := repo.created[0]
	require.NotEqual(t, source.ID, duplicate.ID)
	require.Equal(t, "primary (Copy)", duplicate.Name)
	require.Equal(t, source.Provider, duplicate.Provider)
	require.Equal(t, source.APIMode, duplicate.APIMode)
	require.Equal(t, source.Endpoint, duplicate.Endpoint)
	require.Equal(t, "top-secret", duplicate.APIKey)
	require.Equal(t, "NEW:top-secret", stored.APIKey)
	require.Equal(t, source.PrimaryModel, duplicate.PrimaryModel)
	require.Equal(t, source.ExtraModels, duplicate.ExtraModels)
	require.Equal(t, source.GroupName, duplicate.GroupName)
	require.Equal(t, source.IntervalSeconds, duplicate.IntervalSeconds)
	require.Equal(t, source.JitterSeconds, duplicate.JitterSeconds)
	require.Equal(t, source.TemplateID, duplicate.TemplateID)
	require.Equal(t, source.ExtraHeaders, duplicate.ExtraHeaders)
	require.Equal(t, source.BodyOverrideMode, duplicate.BodyOverrideMode)
	require.Equal(t, source.BodyOverride, duplicate.BodyOverride)
	require.False(t, duplicate.Enabled)
	require.Nil(t, duplicate.LastCheckedAt)
	require.Equal(t, int64(77), duplicate.CreatedBy)
	require.False(t, duplicate.APIKeyDecryptFailed)
	require.NotEmpty(t, duplicate.DuplicateOperationID)

	duplicate.ExtraModels[0] = "changed"
	duplicate.ExtraHeaders["User-Agent"] = "changed"
	duplicate.BodyOverride["metadata"].(map[string]any)["source"] = "changed"
	*duplicate.TemplateID = 10
	require.Equal(t, []string{"gpt-5.4", "gpt-5.3"}, source.ExtraModels)
	require.Equal(t, "Codex", source.ExtraHeaders["User-Agent"])
	require.Equal(t, "original", source.BodyOverride["metadata"].(map[string]any)["source"])
	require.Equal(t, int64(9), *source.TemplateID)
	require.Equal(t, "OLD:top-secret", source.APIKey)
	require.True(t, source.Enabled)
	require.Equal(t, &lastCheckedAt, source.LastCheckedAt)
}

func TestDuplicateChannelMonitorNamePreservesSuffixWithinSchemaLimit(t *testing.T) {
	name := duplicateChannelMonitorName(strings.Repeat("界", 100))

	require.Equal(t, 100, utf8.RuneCountInString(name))
	require.True(t, strings.HasSuffix(name, " (Copy)"))
}

func TestDuplicateChannelMonitorRejectsUndecryptableAPIKey(t *testing.T) {
	source := &ChannelMonitor{ID: 42, Name: "broken", APIKey: "OLD:broken"}
	repo := &duplicateChannelMonitorRepoStub{source: source}
	service := NewChannelMonitorService(repo, &duplicateChannelMonitorEncryptor{decryptErr: errors.New("wrong encryption key")})

	duplicate, err := service.Duplicate(context.Background(), source.ID, 77, "admin:77", "copy-broken")

	require.Nil(t, duplicate)
	require.ErrorIs(t, err, ErrChannelMonitorAPIKeyDecryptFailed)
	require.Empty(t, repo.created)
	require.Equal(t, "OLD:broken", source.APIKey)
}

func TestDuplicateChannelMonitorPropagatesEncryptionAndCreateErrors(t *testing.T) {
	source := &ChannelMonitor{
		ID:              42,
		Name:            "primary",
		APIKey:          "OLD:top-secret",
		PrimaryModel:    "gpt-5.4-mini",
		IntervalSeconds: 60,
	}

	encryptErr := errors.New("encrypt unavailable")
	service := NewChannelMonitorService(
		&duplicateChannelMonitorRepoStub{source: source},
		&duplicateChannelMonitorEncryptor{encryptErr: encryptErr},
	)
	duplicate, err := service.Duplicate(context.Background(), source.ID, 77, "admin:77", "encrypt-error")
	require.Nil(t, duplicate)
	require.ErrorIs(t, err, encryptErr)

	createErr := errors.New("insert unavailable")
	service = NewChannelMonitorService(
		&duplicateChannelMonitorRepoStub{source: source, createErr: createErr},
		&duplicateChannelMonitorEncryptor{},
	)
	duplicate, err = service.Duplicate(context.Background(), source.ID, 77, "admin:77", "create-error")
	require.Nil(t, duplicate)
	require.ErrorContains(t, err, "duplicate channel monitor")
	require.ErrorIs(t, err, createErr)
}

func TestDuplicateChannelMonitorPropagatesCloneAndRecoveryErrors(t *testing.T) {
	source := &ChannelMonitor{
		ID:              42,
		Name:            "primary",
		APIKey:          "OLD:top-secret",
		PrimaryModel:    "gpt-5.4-mini",
		IntervalSeconds: 60,
		BodyOverride:    map[string]any{"unsupported": func() {}},
	}
	service := NewChannelMonitorService(
		&duplicateChannelMonitorRepoStub{source: source},
		&duplicateChannelMonitorEncryptor{},
	)
	duplicate, err := service.Duplicate(context.Background(), source.ID, 77, "admin:77", "clone-error")
	require.Nil(t, duplicate)
	require.ErrorContains(t, err, "clone duplicate channel monitor body override")

	recoveryErr := errors.New("lookup unavailable")
	service = NewChannelMonitorService(
		&duplicateChannelMonitorRepoStub{findErr: recoveryErr},
		&duplicateChannelMonitorEncryptor{},
	)
	recovered, err := service.RecoverDuplicate(context.Background(), source.ID, "admin:77", "recovery-error")
	require.Nil(t, recovered)
	require.ErrorContains(t, err, "find duplicate channel monitor operation")
	require.ErrorIs(t, err, recoveryErr)

	recovered, err = service.RecoverDuplicate(context.Background(), source.ID, "admin:77", "")
	require.NoError(t, err)
	require.Nil(t, recovered)
	require.Equal(
		t,
		duplicateChannelMonitorOperationID(source.ID, "admin:0", "stable-key"),
		duplicateChannelMonitorOperationID(source.ID, "", "stable-key"),
	)
}

func TestDuplicateChannelMonitorRecoversCommittedCopyForSameOperation(t *testing.T) {
	source := &ChannelMonitor{
		ID:               42,
		Name:             "primary",
		Provider:         MonitorProviderOpenAI,
		APIMode:          MonitorAPIModeResponses,
		Endpoint:         "https://api.example.com",
		APIKey:           "OLD:top-secret",
		PrimaryModel:     "gpt-5.4-mini",
		IntervalSeconds:  60,
		BodyOverrideMode: MonitorBodyOverrideModeOff,
	}
	repo := &duplicateChannelMonitorRepoStub{source: source}
	service := NewChannelMonitorService(repo, &duplicateChannelMonitorEncryptor{})

	first, err := service.Duplicate(context.Background(), source.ID, 77, "admin:77", "stable-key")
	require.NoError(t, err)
	retry, err := service.Duplicate(context.Background(), source.ID, 77, "admin:77", "stable-key")
	require.NoError(t, err)

	require.Len(t, repo.created, 1, "same operation must not create a second monitor")
	require.Equal(t, first.ID, retry.ID)
	require.Equal(t, "top-secret", retry.APIKey)
	require.Equal(t, first.DuplicateOperationID, retry.DuplicateOperationID)
	require.NotContains(t, retry.ExtraHeaders, ChannelMonitorDuplicateOperationIDMetadataKey)

	otherActor, err := service.Duplicate(context.Background(), source.ID, 88, "admin:88", "stable-key")
	require.NoError(t, err)
	require.NotEqual(t, first.ID, otherActor.ID)
	require.Len(t, repo.created, 2, "operation identity must include the actor scope")
}

func TestChannelMonitorDuplicateOperationMetadataKeyCannotBeSubmittedAsHeader(t *testing.T) {
	err := validateExtraHeaders(map[string]string{
		ChannelMonitorDuplicateOperationIDMetadataKey: "forged-operation",
	})

	require.Error(t, err)
}
