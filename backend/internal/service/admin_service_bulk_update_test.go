//go:build unit

package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type accountRepoStubForBulkUpdate struct {
	accountRepoStub
	bulkUpdateErr    error
	bulkUpdateIDs    []int64
	bulkUpdateInput  AccountBulkUpdate
	createErr        error
	createdAccount   *Account
	bindGroupErrByID map[int64]error
	bindGroupsCalls  []int64
	getByIDsAccounts []*Account
	getByIDsErr      error
	getByIDsCalled   bool
	getByIDsIDs      []int64
	getByIDAccounts  map[int64]*Account
	getByIDErrByID   map[int64]error
	getByIDCalled    []int64
	listByGroupData  map[int64][]Account
	listByGroupErr   map[int64]error
	listData         []Account
	listResult       *pagination.PaginationResult
	listErr          error
	listCalled       bool
	lastListParams   pagination.PaginationParams
	lastListFilters  struct {
		platform    string
		accountType string
		status      string
		search      string
		groupID     int64
		privacyMode string
	}
}

func (s *accountRepoStubForBulkUpdate) Create(_ context.Context, account *Account) error {
	if s.createErr != nil {
		return s.createErr
	}
	account.ID = 100
	s.createdAccount = account
	return nil
}

func (s *accountRepoStubForBulkUpdate) BulkUpdate(_ context.Context, ids []int64, input AccountBulkUpdate) (int64, error) {
	s.bulkUpdateIDs = append([]int64{}, ids...)
	s.bulkUpdateInput = input
	if s.bulkUpdateErr != nil {
		return 0, s.bulkUpdateErr
	}
	return int64(len(ids)), nil
}

func (s *accountRepoStubForBulkUpdate) BindGroups(_ context.Context, accountID int64, _ []int64) error {
	s.bindGroupsCalls = append(s.bindGroupsCalls, accountID)
	if err, ok := s.bindGroupErrByID[accountID]; ok {
		return err
	}
	return nil
}

func (s *accountRepoStubForBulkUpdate) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	s.getByIDsCalled = true
	s.getByIDsIDs = append([]int64{}, ids...)
	if s.getByIDsErr != nil {
		return nil, s.getByIDsErr
	}
	return s.getByIDsAccounts, nil
}

func (s *accountRepoStubForBulkUpdate) GetByID(_ context.Context, id int64) (*Account, error) {
	s.getByIDCalled = append(s.getByIDCalled, id)
	if err, ok := s.getByIDErrByID[id]; ok {
		return nil, err
	}
	if account, ok := s.getByIDAccounts[id]; ok {
		return account, nil
	}
	return nil, errors.New("account not found")
}

func (s *accountRepoStubForBulkUpdate) ListByGroup(_ context.Context, groupID int64) ([]Account, error) {
	if err, ok := s.listByGroupErr[groupID]; ok {
		return nil, err
	}
	if rows, ok := s.listByGroupData[groupID]; ok {
		return rows, nil
	}
	return nil, nil
}

func (s *accountRepoStubForBulkUpdate) ListWithFilters(_ context.Context, params pagination.PaginationParams, platform, accountType, status, search string, groupID int64, privacyMode string) ([]Account, *pagination.PaginationResult, error) {
	s.listCalled = true
	s.lastListParams = params
	s.lastListFilters.platform = platform
	s.lastListFilters.accountType = accountType
	s.lastListFilters.status = status
	s.lastListFilters.search = search
	s.lastListFilters.groupID = groupID
	s.lastListFilters.privacyMode = privacyMode
	if s.listErr != nil {
		return nil, nil, s.listErr
	}
	if s.listResult != nil {
		return s.listData, s.listResult, nil
	}
	return s.listData, &pagination.PaginationResult{Total: int64(len(s.listData))}, nil
}

// TestAdminService_BulkUpdateAccounts_AllSuccessIDs 验证批量更新成功时返回 success_ids/failed_ids。
func TestAdminService_BulkUpdateAccounts_AllSuccessIDs(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{}
	svc := &adminServiceImpl{accountRepo: repo}

	schedulable := true
	input := &BulkUpdateAccountsInput{
		AccountIDs:  []int64{1, 2, 3},
		Schedulable: &schedulable,
	}

	result, err := svc.BulkUpdateAccounts(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, 3, result.Success)
	require.Equal(t, 0, result.Failed)
	require.ElementsMatch(t, []int64{1, 2, 3}, result.SuccessIDs)
	require.Empty(t, result.FailedIDs)
	require.Len(t, result.Results, 3)
}

// TestAdminService_BulkUpdateAccounts_PartialFailureIDs 验证部分失败时 success_ids/failed_ids 正确。
func TestAdminService_BulkUpdateAccounts_PartialFailureIDs(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{
		bindGroupErrByID: map[int64]error{
			2: errors.New("bind failed"),
		},
	}
	svc := &adminServiceImpl{
		accountRepo: repo,
		groupRepo:   &groupRepoStubForAdmin{getByID: &Group{ID: 10, Name: "g10"}},
	}

	groupIDs := []int64{10}
	schedulable := false
	input := &BulkUpdateAccountsInput{
		AccountIDs:            []int64{1, 2, 3},
		GroupIDs:              &groupIDs,
		Schedulable:           &schedulable,
		SkipMixedChannelCheck: true,
	}

	result, err := svc.BulkUpdateAccounts(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, 2, result.Success)
	require.Equal(t, 1, result.Failed)
	require.ElementsMatch(t, []int64{1, 3}, result.SuccessIDs)
	require.ElementsMatch(t, []int64{2}, result.FailedIDs)
	require.Len(t, result.Results, 3)
}

func TestAdminService_BulkUpdateAccounts_NilGroupRepoReturnsError(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{}
	svc := &adminServiceImpl{accountRepo: repo}

	groupIDs := []int64{10}
	input := &BulkUpdateAccountsInput{
		AccountIDs: []int64{1},
		GroupIDs:   &groupIDs,
	}

	result, err := svc.BulkUpdateAccounts(context.Background(), input)
	require.Nil(t, result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "group repository not configured")
}

// TestAdminService_BulkUpdateAccounts_MixedChannelPreCheckBlocksOnExistingConflict verifies
// that the global pre-check detects a conflict with existing group members and returns an
// error before any DB write is performed.
func TestAdminService_BulkUpdateAccounts_MixedChannelPreCheckBlocksOnExistingConflict(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{
		getByIDsAccounts: []*Account{
			{ID: 1, Platform: PlatformAntigravity},
		},
		// Group 10 already contains an Anthropic account.
		listByGroupData: map[int64][]Account{
			10: {{ID: 99, Platform: PlatformAnthropic}},
		},
	}
	svc := &adminServiceImpl{
		accountRepo: repo,
		groupRepo:   &groupRepoStubForAdmin{getByID: &Group{ID: 10, Name: "target-group"}},
	}

	groupIDs := []int64{10}
	input := &BulkUpdateAccountsInput{
		AccountIDs: []int64{1},
		GroupIDs:   &groupIDs,
	}

	result, err := svc.BulkUpdateAccounts(context.Background(), input)
	require.Nil(t, result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "mixed channel")
	// No BindGroups should have been called since the check runs before any write.
	require.Empty(t, repo.bindGroupsCalls)
}

func TestAdminServiceBulkUpdateAccounts_ResolvesIDsFromFilters(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{
		listData: []Account{
			{ID: 7},
			{ID: 11},
		},
		listResult: &pagination.PaginationResult{Total: 2},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	schedulable := true
	input := &BulkUpdateAccountsInput{
		Schedulable: &schedulable,
	}

	filtersField := reflect.ValueOf(input).Elem().FieldByName("Filters")
	require.True(t, filtersField.IsValid(), "BulkUpdateAccountsInput should expose Filters for filter-target bulk update")
	require.Equal(t, reflect.Ptr, filtersField.Kind(), "BulkUpdateAccountsInput.Filters should be a pointer field")

	filtersValue := reflect.New(filtersField.Type().Elem())
	filtersValue.Elem().FieldByName("Platform").SetString(PlatformOpenAI)
	filtersValue.Elem().FieldByName("Type").SetString(AccountTypeOAuth)
	filtersValue.Elem().FieldByName("Status").SetString(StatusActive)
	filtersValue.Elem().FieldByName("Group").SetString("12")
	filtersValue.Elem().FieldByName("PrivacyMode").SetString(PrivacyModeCFBlocked)
	filtersValue.Elem().FieldByName("Search").SetString("bulk-target")
	filtersField.Set(filtersValue)

	result, err := svc.BulkUpdateAccounts(context.Background(), input)
	require.NoError(t, err)
	require.True(t, repo.listCalled, "expected filter-target bulk update to resolve matching IDs via account list filters")
	require.Equal(t, PlatformOpenAI, repo.lastListFilters.platform)
	require.Equal(t, AccountTypeOAuth, repo.lastListFilters.accountType)
	require.Equal(t, StatusActive, repo.lastListFilters.status)
	require.Equal(t, "bulk-target", repo.lastListFilters.search)
	require.Equal(t, int64(12), repo.lastListFilters.groupID)
	require.Equal(t, PrivacyModeCFBlocked, repo.lastListFilters.privacyMode)
	require.Equal(t, []int64{7, 11}, repo.bulkUpdateIDs)
	require.Equal(t, 2, result.Success)
	require.Equal(t, 0, result.Failed)
	require.Equal(t, []int64{7, 11}, result.SuccessIDs)
}

func TestAdminServiceCreateAccountTypedProbeRejectsInvalidAccount(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{}
	svc := &adminServiceImpl{accountRepo: repo}
	enabled := true

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Platform:             PlatformAnthropic,
		Type:                 AccountTypeAPIKey,
		ProbeEnabled:         &enabled,
		SkipDefaultGroupBind: true,
	})

	require.Nil(t, account)
	require.ErrorIs(t, err, ErrUpstreamBillingProbeAccountInvalid)
	require.Nil(t, repo.createdAccount)
}

func TestAdminServiceCreateAccountTypedProbeSanitizesManagedExtra(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{}
	svc := &adminServiceImpl{accountRepo: repo}
	enabled := true

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Platform:             PlatformOpenAI,
		Type:                 AccountTypeAPIKey,
		Extra:                map[string]any{"safe": "value", UpstreamBillingProbeExtraKey: map[string]any{"status": UpstreamBillingProbeStatusOK}},
		ProbeEnabled:         &enabled,
		SkipDefaultGroupBind: true,
	})

	require.NoError(t, err)
	require.Same(t, repo.createdAccount, account)
	require.Equal(t, "value", account.Extra["safe"])
	require.Equal(t, true, account.Extra[UpstreamBillingProbeEnabledExtraKey])
	require.NotContains(t, account.Extra, UpstreamBillingProbeExtraKey)
}

func TestAdminServiceBulkUpdateTypedProbeRejectsMixedTargets(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{
		getByIDsAccounts: []*Account{
			{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}
	enabled := true

	result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs:   []int64{1, 2},
		ProbeEnabled: &enabled,
	})

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrUpstreamBillingProbeAccountInvalid)
	require.Empty(t, repo.bulkUpdateIDs)
}

func TestAdminServiceBulkUpdateTypedProbeSanitizesManagedExtra(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{
		getByIDsAccounts: []*Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}},
	}
	svc := &adminServiceImpl{accountRepo: repo}
	enabled := false

	result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{1},
		Extra: map[string]any{
			"safe":                              "value",
			UpstreamBillingProbeEnabledExtraKey: true,
			UpstreamBillingProbeExtraKey:        map[string]any{"status": UpstreamBillingProbeStatusOK},
		},
		ProbeEnabled: &enabled,
	})

	require.NoError(t, err)
	require.Equal(t, 1, result.Success)
	require.Equal(t, "value", repo.bulkUpdateInput.Extra["safe"])
	require.Equal(t, false, repo.bulkUpdateInput.Extra[UpstreamBillingProbeEnabledExtraKey])
	require.NotContains(t, repo.bulkUpdateInput.Extra, UpstreamBillingProbeExtraKey)
	require.NotNil(t, repo.bulkUpdateInput.ProbeEnabled)
	require.False(t, *repo.bulkUpdateInput.ProbeEnabled)
}

// ---- normalizeBulkOpenAIEndpointCapabilities ----

func TestNormalizeBulkOpenAIEndpointCapabilities(t *testing.T) {
	tests := []struct {
		name        string
		raw         any
		wantValue   any
		wantHasChat bool
		wantErr     bool
	}{
		{name: "nil clears override", raw: nil, wantValue: nil, wantHasChat: true},
		{name: "chat only", raw: []any{"chat_completions"}, wantValue: []string{"chat_completions"}, wantHasChat: true},
		{name: "embeddings only", raw: []any{"embeddings"}, wantValue: []string{"embeddings"}, wantHasChat: false},
		{name: "both collapse to nil", raw: []any{"chat_completions", "embeddings"}, wantValue: nil, wantHasChat: true},
		{name: "typed string slice", raw: []string{"embeddings"}, wantValue: []string{"embeddings"}, wantHasChat: false},
		{name: "non-string item", raw: []any{123}, wantErr: true},
		{name: "unknown capability", raw: []any{"responses"}, wantErr: true},
		{name: "empty list", raw: []any{}, wantErr: true},
		{name: "wrong type", raw: "chat_completions", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, hasChat, err := normalizeBulkOpenAIEndpointCapabilities(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				require.Equal(t, "OPENAI_ENDPOINT_CAPABILITIES_INVALID", infraerrors.FromError(err).Reason)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantValue, value)
			require.Equal(t, tt.wantHasChat, hasChat)
		})
	}
}

// ---- normalizeBulkOpenAIResponsesMode ----

func TestNormalizeBulkOpenAIResponsesMode(t *testing.T) {
	tests := []struct {
		name       string
		raw        any
		wantValue  any
		wantForced bool
		wantErr    bool
	}{
		{name: "nil keeps auto", raw: nil, wantValue: nil, wantForced: false},
		{name: "auto normalizes to nil", raw: string(openai_compat.ResponsesSupportModeAuto), wantValue: nil, wantForced: false},
		{name: "force responses", raw: string(openai_compat.ResponsesSupportModeForceResponses), wantValue: string(openai_compat.ResponsesSupportModeForceResponses), wantForced: true},
		{name: "force chat completions", raw: string(openai_compat.ResponsesSupportModeForceChatCompletions), wantValue: string(openai_compat.ResponsesSupportModeForceChatCompletions), wantForced: true},
		{name: "wrong type", raw: true, wantErr: true},
		{name: "unknown value", raw: "sometimes", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, forced, err := normalizeBulkOpenAIResponsesMode(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				require.Equal(t, "OPENAI_RESPONSES_MODE_INVALID", infraerrors.FromError(err).Reason)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantValue, value)
			require.Equal(t, tt.wantForced, forced)
		})
	}
}

// ---- normalizeBulkOpenAISettings ----

func TestNormalizeBulkOpenAISettings_NilInputIsNoop(t *testing.T) {
	settings, err := normalizeBulkOpenAISettings(nil)
	require.NoError(t, err)
	require.False(t, settings.any())
}

func TestNormalizeBulkOpenAISettings_NoRelevantFieldsIsNoop(t *testing.T) {
	input := &BulkUpdateAccountsInput{AccountIDs: []int64{1}}
	settings, err := normalizeBulkOpenAISettings(input)
	require.NoError(t, err)
	require.False(t, settings.any())
}

func TestNormalizeBulkOpenAISettings_EmbeddingsOnlyClearsResponsesMode(t *testing.T) {
	input := &BulkUpdateAccountsInput{
		Credentials: map[string]any{openAIEndpointCapabilitiesCredentialKey: []any{"embeddings"}},
	}

	settings, err := normalizeBulkOpenAISettings(input)

	require.NoError(t, err)
	require.True(t, settings.endpointCapabilities)
	require.False(t, settings.capabilitiesIncludeChat)
	require.True(t, settings.responsesMode)
	require.False(t, settings.forcedResponsesMode)
	require.Contains(t, input.Extra, openai_compat.ExtraKeyResponsesMode)
	require.Nil(t, input.Extra[openai_compat.ExtraKeyResponsesMode])
}

func TestNormalizeBulkOpenAISettings_EmbeddingsOnlyConflictsWithForcedResponsesMode(t *testing.T) {
	input := &BulkUpdateAccountsInput{
		Credentials: map[string]any{openAIEndpointCapabilitiesCredentialKey: []any{"embeddings"}},
		Extra:       map[string]any{openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceResponses)},
	}

	_, err := normalizeBulkOpenAISettings(input)

	require.Error(t, err)
	require.Equal(t, "OPENAI_RESPONSES_MODE_INVALID", infraerrors.FromError(err).Reason)
}

func TestNormalizeBulkOpenAISettings_PropagatesCapabilitiesError(t *testing.T) {
	input := &BulkUpdateAccountsInput{
		Credentials: map[string]any{openAIEndpointCapabilitiesCredentialKey: "chat_completions"},
	}

	_, err := normalizeBulkOpenAISettings(input)

	require.Error(t, err)
	require.Equal(t, "OPENAI_ENDPOINT_CAPABILITIES_INVALID", infraerrors.FromError(err).Reason)
}

func TestNormalizeBulkOpenAISettings_PropagatesResponsesModeError(t *testing.T) {
	input := &BulkUpdateAccountsInput{
		Extra: map[string]any{openai_compat.ExtraKeyResponsesMode: "sometimes"},
	}

	_, err := normalizeBulkOpenAISettings(input)

	require.Error(t, err)
	require.Equal(t, "OPENAI_RESPONSES_MODE_INVALID", infraerrors.FromError(err).Reason)
}

// ---- validateBulkOpenAISettingsTargets ----

func TestValidateBulkOpenAISettingsTargets_NoopWhenSettingsEmpty(t *testing.T) {
	err := validateBulkOpenAISettingsTargets(&BulkUpdateAccountsInput{AccountIDs: []int64{1}}, bulkOpenAISettings{}, nil)
	require.NoError(t, err)
}

func TestValidateBulkOpenAISettingsTargets_NoopWhenInputNil(t *testing.T) {
	err := validateBulkOpenAISettingsTargets(nil, bulkOpenAISettings{endpointCapabilities: true}, nil)
	require.NoError(t, err)
}

func TestValidateBulkOpenAISettingsTargets_RejectsMissingAccount(t *testing.T) {
	err := validateBulkOpenAISettingsTargets(
		&BulkUpdateAccountsInput{AccountIDs: []int64{1}},
		bulkOpenAISettings{endpointCapabilities: true},
		map[int64]*Account{},
	)

	require.Error(t, err)
	appErr := infraerrors.FromError(err)
	require.Equal(t, "OPENAI_BULK_TARGET_INVALID", appErr.Reason)
	require.Equal(t, "1", appErr.Metadata["account_id"])
}

func TestValidateBulkOpenAISettingsTargets_RejectsNonOpenAIPlatform(t *testing.T) {
	err := validateBulkOpenAISettingsTargets(
		&BulkUpdateAccountsInput{AccountIDs: []int64{1}},
		bulkOpenAISettings{responsesMode: true},
		map[int64]*Account{1: {ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}},
	)

	require.Error(t, err)
	require.Equal(t, "OPENAI_BULK_TARGET_INVALID", infraerrors.FromError(err).Reason)
}

func TestValidateBulkOpenAISettingsTargets_RejectsNonAPIKeyType(t *testing.T) {
	err := validateBulkOpenAISettingsTargets(
		&BulkUpdateAccountsInput{AccountIDs: []int64{1}},
		bulkOpenAISettings{endpointCapabilities: true},
		map[int64]*Account{1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}},
	)

	require.Error(t, err)
	require.Equal(t, "OPENAI_BULK_TARGET_INVALID", infraerrors.FromError(err).Reason)
}

func TestValidateBulkOpenAISettingsTargets_ForcedResponsesRequiresCurrentChatCapability(t *testing.T) {
	err := validateBulkOpenAISettingsTargets(
		&BulkUpdateAccountsInput{AccountIDs: []int64{1}},
		bulkOpenAISettings{responsesMode: true, forcedResponsesMode: true},
		map[int64]*Account{1: {
			ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Credentials: map[string]any{openAIEndpointCapabilitiesCredentialKey: []string{"embeddings"}},
		}},
	)

	require.Error(t, err)
	require.Equal(t, "OPENAI_BULK_TARGET_INVALID", infraerrors.FromError(err).Reason)
}

func TestValidateBulkOpenAISettingsTargets_ForcedResponsesAllowsDefaultChatCapability(t *testing.T) {
	err := validateBulkOpenAISettingsTargets(
		&BulkUpdateAccountsInput{AccountIDs: []int64{1}},
		bulkOpenAISettings{responsesMode: true, forcedResponsesMode: true},
		map[int64]*Account{1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}},
	)

	require.NoError(t, err)
}

func TestValidateBulkOpenAISettingsTargets_ForcedResponsesSkipsCurrentCapabilityCheckWhenUpdatingCapabilities(t *testing.T) {
	err := validateBulkOpenAISettingsTargets(
		&BulkUpdateAccountsInput{AccountIDs: []int64{1}},
		bulkOpenAISettings{endpointCapabilities: true, capabilitiesIncludeChat: true, responsesMode: true, forcedResponsesMode: true},
		map[int64]*Account{1: {
			ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Credentials: map[string]any{openAIEndpointCapabilitiesCredentialKey: []string{"embeddings"}},
		}},
	)

	require.NoError(t, err)
}

// ---- BulkUpdateAccounts integration ----

func TestAdminServiceBulkUpdateAccounts_NormalizesOpenAISettings(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
	}}
	svc := &adminServiceImpl{accountRepo: repo}

	result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{1, 2},
		Credentials: map[string]any{
			openAIEndpointCapabilitiesCredentialKey: []any{"chat_completions", "embeddings"},
		},
		Extra: map[string]any{
			openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeAuto),
		},
	})

	require.NoError(t, err)
	require.Equal(t, 2, result.Success)
	require.Equal(t, []int64{1, 2}, repo.bulkUpdateIDs)
	require.Contains(t, repo.bulkUpdateInput.Credentials, openAIEndpointCapabilitiesCredentialKey)
	require.Nil(t, repo.bulkUpdateInput.Credentials[openAIEndpointCapabilitiesCredentialKey])
	require.Contains(t, repo.bulkUpdateInput.Extra, openai_compat.ExtraKeyResponsesMode)
	require.Nil(t, repo.bulkUpdateInput.Extra[openai_compat.ExtraKeyResponsesMode])
}

func TestAdminServiceBulkUpdateAccounts_EmbeddingsOnlyResetsResponsesMode(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
	}}
	svc := &adminServiceImpl{accountRepo: repo}

	_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{1},
		Credentials: map[string]any{
			openAIEndpointCapabilitiesCredentialKey: []string{"embeddings"},
		},
	})

	require.NoError(t, err)
	require.Equal(t, []string{"embeddings"}, repo.bulkUpdateInput.Credentials[openAIEndpointCapabilitiesCredentialKey])
	require.Contains(t, repo.bulkUpdateInput.Extra, openai_compat.ExtraKeyResponsesMode)
	require.Nil(t, repo.bulkUpdateInput.Extra[openai_compat.ExtraKeyResponsesMode])
}

func TestAdminServiceBulkUpdateAccounts_RejectsInvalidOpenAISettingValuesBeforeWrite(t *testing.T) {
	tests := []struct {
		name        string
		credentials map[string]any
		extra       map[string]any
		reason      string
	}{
		{name: "empty capabilities", credentials: map[string]any{openAIEndpointCapabilitiesCredentialKey: []any{}}, reason: "OPENAI_ENDPOINT_CAPABILITIES_INVALID"},
		{name: "unknown capability", credentials: map[string]any{openAIEndpointCapabilitiesCredentialKey: []any{"responses"}}, reason: "OPENAI_ENDPOINT_CAPABILITIES_INVALID"},
		{name: "capabilities type", credentials: map[string]any{openAIEndpointCapabilitiesCredentialKey: "chat_completions"}, reason: "OPENAI_ENDPOINT_CAPABILITIES_INVALID"},
		{name: "responses mode value", extra: map[string]any{openai_compat.ExtraKeyResponsesMode: "sometimes"}, reason: "OPENAI_RESPONSES_MODE_INVALID"},
		{name: "responses mode type", extra: map[string]any{openai_compat.ExtraKeyResponsesMode: true}, reason: "OPENAI_RESPONSES_MODE_INVALID"},
		{
			name:        "embeddings conflict",
			credentials: map[string]any{openAIEndpointCapabilitiesCredentialKey: []any{"embeddings"}},
			extra:       map[string]any{openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceResponses)},
			reason:      "OPENAI_RESPONSES_MODE_INVALID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &accountRepoStubForBulkUpdate{}
			svc := &adminServiceImpl{accountRepo: repo}
			result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
				AccountIDs:  []int64{1},
				Credentials: tt.credentials,
				Extra:       tt.extra,
			})
			require.Nil(t, result)
			require.Equal(t, tt.reason, infraerrors.FromError(err).Reason)
			require.Empty(t, repo.bulkUpdateIDs)
		})
	}
}

func TestAdminServiceBulkUpdateAccounts_RejectsInvalidOpenAITargetsBeforeWrite(t *testing.T) {
	tests := []struct {
		name     string
		accounts []*Account
		input    *BulkUpdateAccountsInput
	}{
		{
			name:     "missing account",
			accounts: []*Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}},
			input: &BulkUpdateAccountsInput{
				AccountIDs:  []int64{1, 2},
				Credentials: map[string]any{openAIEndpointCapabilitiesCredentialKey: []any{"chat_completions"}},
			},
		},
		{
			name:     "non-OpenAI platform",
			accounts: []*Account{{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}},
			input: &BulkUpdateAccountsInput{
				AccountIDs: []int64{1},
				Extra:      map[string]any{openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions)},
			},
		},
		{
			name:     "non-API-key account type",
			accounts: []*Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}},
			input: &BulkUpdateAccountsInput{
				AccountIDs:  []int64{1},
				Credentials: map[string]any{openAIEndpointCapabilitiesCredentialKey: nil},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &accountRepoStubForBulkUpdate{getByIDsAccounts: tt.accounts}
			svc := &adminServiceImpl{accountRepo: repo}
			result, err := svc.BulkUpdateAccounts(context.Background(), tt.input)
			require.Nil(t, result)
			require.Equal(t, "OPENAI_BULK_TARGET_INVALID", infraerrors.FromError(err).Reason)
			require.Empty(t, repo.bulkUpdateIDs)
		})
	}
}

func TestAdminServiceBulkUpdateAccounts_ForcedResponsesRequiresChatCapability(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{{
		ID:       1,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			openAIEndpointCapabilitiesCredentialKey: []any{"embeddings"},
		},
	}}}
	svc := &adminServiceImpl{accountRepo: repo}

	result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{1},
		Extra:      map[string]any{openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions)},
	})

	require.Nil(t, result)
	require.Equal(t, "OPENAI_BULK_TARGET_INVALID", infraerrors.FromError(err).Reason)
	require.Empty(t, repo.bulkUpdateIDs)
}

func TestAdminServiceBulkUpdateAccounts_ForcedResponsesAcceptsChatCapabilityUpdate(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{{
		ID:       1,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			openAIEndpointCapabilitiesCredentialKey: []any{"embeddings"},
		},
	}}}
	svc := &adminServiceImpl{accountRepo: repo}

	_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{1},
		Credentials: map[string]any{
			openAIEndpointCapabilitiesCredentialKey: []any{"chat_completions"},
		},
		Extra: map[string]any{openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceResponses)},
	})

	require.NoError(t, err)
	require.Equal(t, []int64{1}, repo.bulkUpdateIDs)
}

func TestAdminServiceBulkUpdateAccounts_ValidatesFilterResolvedOpenAITargets(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{
		listData:         []Account{{ID: 7}},
		listResult:       &pagination.PaginationResult{Total: 1},
		getByIDsAccounts: []*Account{{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		Filters: &BulkUpdateAccountFilters{Platform: PlatformOpenAI},
		Extra:   map[string]any{openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions)},
	})

	require.Nil(t, result)
	require.Equal(t, "OPENAI_BULK_TARGET_INVALID", infraerrors.FromError(err).Reason)
	require.Equal(t, []int64{7}, repo.getByIDsIDs)
	require.Empty(t, repo.bulkUpdateIDs)
}
