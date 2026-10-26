package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLogFromService_IncludesOpenAIWSMode(t *testing.T) {
	t.Parallel()

	wsLog := &service.UsageLog{
		RequestID:    "req_1",
		Model:        "gpt-5.3-codex",
		OpenAIWSMode: true,
	}
	httpLog := &service.UsageLog{
		RequestID:    "resp_1",
		Model:        "gpt-5.3-codex",
		OpenAIWSMode: false,
	}

	require.True(t, UsageLogFromService(wsLog).OpenAIWSMode)
	require.False(t, UsageLogFromService(httpLog).OpenAIWSMode)
	require.True(t, UsageLogFromServiceAdmin(wsLog).OpenAIWSMode)
	require.False(t, UsageLogFromServiceAdmin(httpLog).OpenAIWSMode)
}

func TestUsageLogFromService_PrefersRequestTypeForLegacyFields(t *testing.T) {
	t.Parallel()

	log := &service.UsageLog{
		RequestID:    "req_2",
		Model:        "gpt-5.3-codex",
		RequestType:  service.RequestTypeWSV2,
		Stream:       false,
		OpenAIWSMode: false,
	}

	userDTO := UsageLogFromService(log)
	adminDTO := UsageLogFromServiceAdmin(log)

	require.Equal(t, "ws_v2", userDTO.RequestType)
	require.True(t, userDTO.Stream)
	require.True(t, userDTO.OpenAIWSMode)
	require.Equal(t, "ws_v2", adminDTO.RequestType)
	require.True(t, adminDTO.Stream)
	require.True(t, adminDTO.OpenAIWSMode)
}

func TestUsageCleanupTaskFromService_RequestTypeMapping(t *testing.T) {
	t.Parallel()

	requestType := int16(service.RequestTypeStream)
	task := &service.UsageCleanupTask{
		ID:     1,
		Status: service.UsageCleanupStatusPending,
		Filters: service.UsageCleanupFilters{
			RequestType: &requestType,
		},
	}

	dtoTask := UsageCleanupTaskFromService(task)
	require.NotNil(t, dtoTask)
	require.NotNil(t, dtoTask.Filters.RequestType)
	require.Equal(t, "stream", *dtoTask.Filters.RequestType)
}

func TestRequestTypeStringPtrNil(t *testing.T) {
	t.Parallel()
	require.Nil(t, requestTypeStringPtr(nil))
}

func TestUsageLogFromService_IncludesServiceTierForUserAndAdmin(t *testing.T) {
	t.Parallel()

	serviceTier := "priority"
	inboundEndpoint := "/v1/chat/completions"
	upstreamEndpoint := "/v1/responses"
	log := &service.UsageLog{
		RequestID:             "req_3",
		Model:                 "gpt-5.4",
		ServiceTier:           &serviceTier,
		InboundEndpoint:       &inboundEndpoint,
		UpstreamEndpoint:      &upstreamEndpoint,
		AccountRateMultiplier: f64Ptr(1.5),
	}

	userDTO := UsageLogFromService(log)
	adminDTO := UsageLogFromServiceAdmin(log)

	require.NotNil(t, userDTO.ServiceTier)
	require.Equal(t, serviceTier, *userDTO.ServiceTier)
	require.NotNil(t, userDTO.InboundEndpoint)
	require.Equal(t, inboundEndpoint, *userDTO.InboundEndpoint)
	require.NotNil(t, userDTO.UpstreamEndpoint)
	require.Equal(t, upstreamEndpoint, *userDTO.UpstreamEndpoint)
	require.NotNil(t, adminDTO.ServiceTier)
	require.Equal(t, serviceTier, *adminDTO.ServiceTier)
	require.NotNil(t, adminDTO.InboundEndpoint)
	require.Equal(t, inboundEndpoint, *adminDTO.InboundEndpoint)
	require.NotNil(t, adminDTO.UpstreamEndpoint)
	require.Equal(t, upstreamEndpoint, *adminDTO.UpstreamEndpoint)
	require.NotNil(t, adminDTO.AccountRateMultiplier)
	require.InDelta(t, 1.5, *adminDTO.AccountRateMultiplier, 1e-12)
}

func TestUsageLogFromService_UsesRequestedModelAndKeepsUpstreamAdminOnly(t *testing.T) {
	t.Parallel()

	upstreamModel := "claude-sonnet-4-20250514"
	log := &service.UsageLog{
		RequestID:      "req_4",
		Model:          upstreamModel,
		RequestedModel: "claude-sonnet-4",
		UpstreamModel:  &upstreamModel,
	}

	userDTO := UsageLogFromService(log)
	adminDTO := UsageLogFromServiceAdmin(log)

	require.Equal(t, "claude-sonnet-4", userDTO.Model)
	require.Equal(t, "claude-sonnet-4", adminDTO.Model)

	userJSON, err := json.Marshal(userDTO)
	require.NoError(t, err)
	require.NotContains(t, string(userJSON), "upstream_model")

	adminJSON, err := json.Marshal(adminDTO)
	require.NoError(t, err)
	require.Contains(t, string(adminJSON), `"upstream_model":"claude-sonnet-4-20250514"`)
}

func TestUsageLogFromService_FallsBackToLegacyModelWhenRequestedModelMissing(t *testing.T) {
	t.Parallel()

	log := &service.UsageLog{
		RequestID: "req_legacy",
		Model:     "claude-3",
	}

	userDTO := UsageLogFromService(log)
	adminDTO := UsageLogFromServiceAdmin(log)

	require.Equal(t, "claude-3", userDTO.Model)
	require.Equal(t, "claude-3", adminDTO.Model)
}

func TestUsageLogFromService_IncludesImageBillingMetadataForUserAndAdmin(t *testing.T) {
	t.Parallel()

	imageSize := "4K"
	inputSize := "1024x1024"
	outputSize := "3840x2160"
	source := "output"
	log := &service.UsageLog{
		RequestID:          "req_image_metadata",
		Model:              "gpt-image-2",
		ImageCount:         2,
		ImageSize:          &imageSize,
		ImageInputSize:     &inputSize,
		ImageOutputSize:    &outputSize,
		ImageSizeSource:    &source,
		ImageSizeBreakdown: map[string]int{"4K": 2},
	}

	userDTO := UsageLogFromService(log)
	adminDTO := UsageLogFromServiceAdmin(log)

	for _, got := range []*UsageLog{userDTO, &adminDTO.UsageLog} {
		require.Equal(t, 2, got.ImageCount)
		require.NotNil(t, got.ImageSize)
		require.Equal(t, imageSize, *got.ImageSize)
		require.NotNil(t, got.ImageInputSize)
		require.Equal(t, inputSize, *got.ImageInputSize)
		require.NotNil(t, got.ImageOutputSize)
		require.Equal(t, outputSize, *got.ImageOutputSize)
		require.NotNil(t, got.ImageSizeSource)
		require.Equal(t, source, *got.ImageSizeSource)
		require.Equal(t, map[string]int{"4K": 2}, got.ImageSizeBreakdown)
	}
}

func TestUsageLogFromService_PreservesHistoricalMissingImageSize(t *testing.T) {
	t.Parallel()

	log := &service.UsageLog{
		RequestID:  "req_legacy_image_missing_size",
		Model:      "gpt-image-2",
		ImageCount: 1,
		ImageSize:  nil,
	}

	dto := UsageLogFromService(log)
	require.Equal(t, 1, dto.ImageCount)
	require.Nil(t, dto.ImageSize)
	require.Nil(t, dto.ImageInputSize)
	require.Nil(t, dto.ImageOutputSize)
	require.Nil(t, dto.ImageSizeSource)
	require.Nil(t, dto.ImageSizeBreakdown)

	body, err := json.Marshal(dto)
	require.NoError(t, err)
	require.Contains(t, string(body), `"image_size":null`)
	require.NotContains(t, string(body), `"image_size":"2K"`)
}

func TestUsageLogFromServiceAdmin_IncludesUpstreamModelMismatchFields(t *testing.T) {
	t.Parallel()

	responseModel := "claude-sonnet-4-20250514"
	mismatchLog := &service.UsageLog{
		RequestID:             "req_mismatch",
		Model:                 "claude-sonnet-4",
		UpstreamModelMismatch: true,
		UpstreamResponseModel: &responseModel,
	}
	matchLog := &service.UsageLog{
		RequestID:             "req_match",
		Model:                 "claude-sonnet-4",
		UpstreamModelMismatch: false,
		UpstreamResponseModel: nil,
	}

	mismatchDTO := UsageLogFromServiceAdmin(mismatchLog)
	require.True(t, mismatchDTO.UpstreamModelMismatch)
	require.NotNil(t, mismatchDTO.UpstreamResponseModel)
	require.Equal(t, responseModel, *mismatchDTO.UpstreamResponseModel)

	mismatchJSON, err := json.Marshal(mismatchDTO)
	require.NoError(t, err)
	require.Contains(t, string(mismatchJSON), `"upstream_model_mismatch":true`)
	require.Contains(t, string(mismatchJSON), `"upstream_response_model":"claude-sonnet-4-20250514"`)

	matchDTO := UsageLogFromServiceAdmin(matchLog)
	require.False(t, matchDTO.UpstreamModelMismatch)
	require.Nil(t, matchDTO.UpstreamResponseModel)

	matchJSON, err := json.Marshal(matchDTO)
	require.NoError(t, err)
	require.Contains(t, string(matchJSON), `"upstream_model_mismatch":false`)
	require.NotContains(t, string(matchJSON), `"upstream_response_model"`)
}

func f64Ptr(value float64) *float64 {
	return &value
}

func TestUsageLogFromService_ServedGroupHiddenUnlessUserChain(t *testing.T) {
	t.Parallel()

	served := int64(21)
	i16 := func(v int16) *int16 { return &v }
	cases := []struct {
		name       string
		groupID    *int64
		source     *int16
		wantUser   *int64
		wantAdmin  *int64
		wantSource *int16
	}{
		{"no fallback", nil, nil, nil, nil, nil},
		{"user chain", &served, i16(1), &served, &served, i16(1)},
		{"admin hidden chain is hidden from users", &served, i16(2), nil, &served, i16(2)},
		{"unknown source is hidden from users", &served, i16(9), nil, &served, i16(9)},
		{"served without source is hidden from users", &served, nil, nil, &served, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := &service.UsageLog{RequestID: "req", Model: "gpt-5", ServedGroupID: tc.groupID, ServedRouteSource: tc.source}

			userDTO := UsageLogFromService(log)
			require.Equal(t, tc.wantUser, userDTO.ServedGroupID)
			userJSON, err := json.Marshal(userDTO)
			require.NoError(t, err)
			require.NotContains(t, string(userJSON), "served_route_source")
			if tc.wantUser == nil {
				require.NotContains(t, string(userJSON), "served_group_id")
			} else {
				require.Contains(t, string(userJSON), `"served_group_id":21`)
			}

			adminDTO := UsageLogFromServiceAdmin(log)
			require.Equal(t, tc.wantAdmin, adminDTO.ServedGroupID)
			require.Equal(t, tc.wantSource, adminDTO.ServedRouteSource)
			adminJSON, err := json.Marshal(adminDTO)
			require.NoError(t, err)
			if tc.wantAdmin != nil {
				require.Contains(t, string(adminJSON), `"served_group_id":21`)
			}
			if tc.wantSource != nil {
				require.Contains(t, string(adminJSON), `"served_route_source"`)
			}
		})
	}
}

func TestUsageLogFromService_ServedGroupObjectFollowsSameVisibilityRule(t *testing.T) {
	t.Parallel()

	served := int64(21)
	i16 := func(v int16) *int16 { return &v }
	group := &service.Group{ID: 21, Name: "fallback-group", Platform: "openai", Status: "active"}

	// 用户链：用户端与管理端都带 served_group 对象。
	userChain := &service.UsageLog{RequestID: "req", Model: "gpt-5", ServedGroupID: &served, ServedRouteSource: i16(1), ServedGroup: group}
	userDTO := UsageLogFromService(userChain)
	require.NotNil(t, userDTO.ServedGroup)
	require.Equal(t, "fallback-group", userDTO.ServedGroup.Name)
	userJSON, err := json.Marshal(userDTO)
	require.NoError(t, err)
	require.Contains(t, string(userJSON), `"served_group":{`)

	// 管理员隐藏链：用户端 JSON 里不得出现 served_group 的任何痕迹（包括分组名）；管理端始终带。
	hidden := &service.UsageLog{RequestID: "req", Model: "gpt-5", ServedGroupID: &served, ServedRouteSource: i16(2), ServedGroup: group}
	hiddenUser := UsageLogFromService(hidden)
	require.Nil(t, hiddenUser.ServedGroup)
	hiddenJSON, err := json.Marshal(hiddenUser)
	require.NoError(t, err)
	require.NotContains(t, string(hiddenJSON), "served_group")
	require.NotContains(t, string(hiddenJSON), "fallback-group")
	hiddenAdmin := UsageLogFromServiceAdmin(hidden)
	require.NotNil(t, hiddenAdmin.ServedGroup)
	require.Equal(t, "fallback-group", hiddenAdmin.ServedGroup.Name)
	require.Equal(t, i16(2), hiddenAdmin.ServedRouteSource)

	// 没有回退：两端都没有 served_group。
	plain := &service.UsageLog{RequestID: "req", Model: "gpt-5"}
	require.Nil(t, UsageLogFromService(plain).ServedGroup)
	require.Nil(t, UsageLogFromServiceAdmin(plain).ServedGroup)

	// 来源是用户链但 served 分组没加载出来（例如分组刚被删除）：不报错，字段缺省。
	unloaded := &service.UsageLog{RequestID: "req", Model: "gpt-5", ServedGroupID: &served, ServedRouteSource: i16(1)}
	require.Nil(t, UsageLogFromService(unloaded).ServedGroup)
	require.Equal(t, &served, UsageLogFromService(unloaded).ServedGroupID)
}
