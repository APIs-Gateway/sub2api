package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
	"go.uber.org/zap"
)

var (
	openAIModelDatePattern = regexp.MustCompile(`-\d{8}$`)
	openAIModelBasePattern = regexp.MustCompile(`^(gpt-\d+(?:\.\d+)?)(?:-|$)`)
	// Official GPT Image 2.5 token rates (2026-09-08):
	// https://developers.openai.com/api/docs/pricing#image-generation-models
	openAIGPTImage25FallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:       5e-06,
		CacheReadInputTokenCost: 1.25e-06,
		InputCostPerImageToken:  8e-06, CacheReadInputImageTokenCost: 2e-06,
		OutputCostPerImageToken: 3e-05,
		LiteLLMProvider:         "openai",
		Mode:                    "image_generation",
		SupportsPromptCaching:   true,
	}
	openAIGPT54FallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:               2.5e-06, // $2.5 per MTok
		OutputCostPerToken:              1.5e-05, // $15 per MTok
		CacheReadInputTokenCost:         2.5e-07, // $0.25 per MTok
		LongContextInputTokenThreshold:  272000,
		LongContextInputCostMultiplier:  2.0,
		LongContextOutputCostMultiplier: 1.5,
		LiteLLMProvider:                 "openai",
		Mode:                            "chat",
		SupportsPromptCaching:           true,
	}
	openAIGPT54MiniFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:       7.5e-07,
		OutputCostPerToken:      4.5e-06,
		CacheReadInputTokenCost: 7.5e-08,
		LiteLLMProvider:         "openai",
		Mode:                    "chat",
		SupportsPromptCaching:   true,
	}
	openAIGPT54NanoFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:       2e-07,
		OutputCostPerToken:      1.25e-06,
		CacheReadInputTokenCost: 2e-08,
		LiteLLMProvider:         "openai",
		Mode:                    "chat",
		SupportsPromptCaching:   true,
	}
	// openAIGPT6AstraFallbackPricing 有意不设置 *Above272KTokens 绝对值字段：
	// gpt-6-astra 的 priority 与长上下文倍率是叠加关系（不像 GPT-5.6 那样互斥），
	// computeTokenBreakdown 在 Above272K 字段 >0 时会用它直接覆盖 inputPrice，
	// 从而丢弃已经生效的 priority 价；只设置 LongContextInput/OutputCostMultiplier
	// 才能让长上下文倍率在任意 tier 上都正确地乘算在当前价格之上。
	openAIGPT6AstraFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:                   1e-05,
		InputCostPerTokenPriority:           2e-05,
		OutputCostPerToken:                  5e-05,
		OutputCostPerTokenPriority:          1e-04,
		CacheCreationInputTokenCost:         1.25e-05,
		CacheCreationInputTokenCostPriority: 2.5e-05,
		CacheReadInputTokenCost:             1e-06,
		CacheReadInputTokenCostPriority:     2e-06,
		LongContextInputTokenThreshold:      272_000,
		LongContextInputCostMultiplier:      2,
		LongContextOutputCostMultiplier:     1.5,
		SupportsServiceTier:                 true,
		LiteLLMProvider:                     "openai",
		Mode:                                "chat",
		SupportsPromptCaching:               true,
	}
	// GPT-6 Sol / Luna 与 Astra 同理：priority 与长上下文倍率叠加，只设倍率字段，
	// 不设 *Above272KTokens 绝对值字段。价格同步自上游 #7509（2026-09-22 官方价）。
	openAIGPT6SolFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:                   2e-06,
		InputCostPerTokenPriority:           4e-06,
		OutputCostPerToken:                  1e-05,
		OutputCostPerTokenPriority:          2e-05,
		CacheCreationInputTokenCost:         2.5e-06,
		CacheCreationInputTokenCostPriority: 5e-06,
		CacheReadInputTokenCost:             2e-07,
		CacheReadInputTokenCostPriority:     4e-07,
		LongContextInputTokenThreshold:      272_000,
		LongContextInputCostMultiplier:      2,
		LongContextOutputCostMultiplier:     1.5,
		SupportsServiceTier:                 true,
		LiteLLMProvider:                     "openai",
		Mode:                                "chat",
		SupportsPromptCaching:               true,
	}
	// GPT-6.1 Sol（同步自上游 9688571a8）：与 GPT-6 Sol 同价，仅 cache read 减半。
	openAIGPT61SolFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:                   2e-06,
		InputCostPerTokenPriority:           4e-06,
		OutputCostPerToken:                  1e-05,
		OutputCostPerTokenPriority:          2e-05,
		CacheCreationInputTokenCost:         2.5e-06,
		CacheCreationInputTokenCostPriority: 5e-06,
		CacheReadInputTokenCost:             1e-07,
		CacheReadInputTokenCostPriority:     2e-07,
		LongContextInputTokenThreshold:      272_000,
		LongContextInputCostMultiplier:      2,
		LongContextOutputCostMultiplier:     1.5,
		SupportsServiceTier:                 true,
		LiteLLMProvider:                     "openai",
		Mode:                                "chat",
		SupportsPromptCaching:               true,
	}
	openAIGPT6LunaFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:                   1e-07,
		InputCostPerTokenPriority:           2e-07,
		OutputCostPerToken:                  5e-07,
		OutputCostPerTokenPriority:          1e-06,
		CacheCreationInputTokenCost:         1.25e-07,
		CacheCreationInputTokenCostPriority: 2.5e-07,
		CacheReadInputTokenCost:             1e-08,
		CacheReadInputTokenCostPriority:     2e-08,
		LongContextInputTokenThreshold:      272_000,
		LongContextInputCostMultiplier:      2,
		LongContextOutputCostMultiplier:     1.5,
		SupportsServiceTier:                 true,
		LiteLLMProvider:                     "openai",
		Mode:                                "chat",
		SupportsPromptCaching:               true,
	}
	// Claude Opus 5.5 官方价（同步自上游 #7509）；priority 为标准价 2 倍（Fast）。
	claudeOpus55FallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:                   4e-06,
		OutputCostPerToken:                  2e-05,
		CacheCreationInputTokenCost:         5e-06,
		CacheCreationInputTokenCostAbove1hr: 8e-06,
		CacheReadInputTokenCost:             2e-07,
		InputCostPerTokenPriority:           8e-06,
		OutputCostPerTokenPriority:          4e-05,
		CacheCreationInputTokenCostPriority: 1e-05,
		CacheReadInputTokenCostPriority:     4e-07,
		SupportsServiceTier:                 true,
		LiteLLMProvider:                     "anthropic",
		Mode:                                "chat",
		SupportsPromptCaching:               true,
	}
	openAIGPT56SolFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:                          4e-06,
		InputCostPerTokenAbove272KTokens:           8e-06,
		InputCostPerTokenPriority:                  8e-06,
		OutputCostPerToken:                         2e-05,
		OutputCostPerTokenAbove272KTokens:          3e-05,
		OutputCostPerTokenPriority:                 4e-05,
		CacheCreationInputTokenCost:                5e-06,
		CacheCreationInputTokenCostAbove272KTokens: 1e-05,
		CacheCreationInputTokenCostPriority:        1e-05,
		CacheReadInputTokenCost:                    4e-07,
		CacheReadInputTokenCostAbove272KTokens:     8e-07,
		CacheReadInputTokenCostPriority:            8e-07,
		LongContextInputTokenThreshold:             gpt56LongContextTokenThreshold,
		LongContextInputCostMultiplier:             gpt56LongContextInputMultiplier,
		LongContextOutputCostMultiplier:            gpt56LongContextOutputMultiplier,
		LiteLLMProvider:                            "openai",
		Mode:                                       "chat",
		SupportsPromptCaching:                      true,
	}
	openAIGPT56TerraFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:                          2e-06,
		InputCostPerTokenAbove272KTokens:           4e-06,
		InputCostPerTokenPriority:                  4e-06,
		OutputCostPerToken:                         1.2e-05,
		OutputCostPerTokenAbove272KTokens:          1.8e-05,
		OutputCostPerTokenPriority:                 2.4e-05,
		CacheCreationInputTokenCost:                2.5e-06,
		CacheCreationInputTokenCostAbove272KTokens: 5e-06,
		CacheCreationInputTokenCostPriority:        5e-06,
		CacheReadInputTokenCost:                    2e-07,
		CacheReadInputTokenCostAbove272KTokens:     4e-07,
		CacheReadInputTokenCostPriority:            4e-07,
		LongContextInputTokenThreshold:             gpt56LongContextTokenThreshold,
		LongContextInputCostMultiplier:             gpt56LongContextInputMultiplier,
		LongContextOutputCostMultiplier:            gpt56LongContextOutputMultiplier,
		LiteLLMProvider:                            "openai",
		Mode:                                       "chat",
		SupportsPromptCaching:                      true,
	}
	openAIGPT56LunaFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:                          2e-07,
		InputCostPerTokenAbove272KTokens:           4e-07,
		InputCostPerTokenPriority:                  4e-07,
		OutputCostPerToken:                         1.2e-06,
		OutputCostPerTokenAbove272KTokens:          1.8e-06,
		OutputCostPerTokenPriority:                 2.4e-06,
		CacheCreationInputTokenCost:                2.5e-07,
		CacheCreationInputTokenCostAbove272KTokens: 5e-07,
		CacheCreationInputTokenCostPriority:        5e-07,
		CacheReadInputTokenCost:                    2e-08,
		CacheReadInputTokenCostAbove272KTokens:     4e-08,
		CacheReadInputTokenCostPriority:            4e-08,
		LongContextInputTokenThreshold:             gpt56LongContextTokenThreshold,
		LongContextInputCostMultiplier:             gpt56LongContextInputMultiplier,
		LongContextOutputCostMultiplier:            gpt56LongContextOutputMultiplier,
		LiteLLMProvider:                            "openai",
		Mode:                                       "chat",
		SupportsPromptCaching:                      true,
	}
)

// LiteLLMModelPricing LiteLLM价格数据结构
// 只保留我们需要的字段，使用指针来处理可能缺失的值
type LiteLLMModelPricing struct {
	InputCostPerToken                          float64 `json:"input_cost_per_token"`
	InputCostPerTokenAbove272KTokens           float64 `json:"input_cost_per_token_above_272k_tokens"`
	InputCostPerTokenPriority                  float64 `json:"input_cost_per_token_priority"`
	OutputCostPerToken                         float64 `json:"output_cost_per_token"`
	OutputCostPerTokenAbove272KTokens          float64 `json:"output_cost_per_token_above_272k_tokens"`
	OutputCostPerTokenPriority                 float64 `json:"output_cost_per_token_priority"`
	CacheCreationInputTokenCost                float64 `json:"cache_creation_input_token_cost"`
	CacheCreationInputTokenCostAbove272KTokens float64 `json:"cache_creation_input_token_cost_above_272k_tokens"`
	CacheCreationInputTokenCostPriority        float64 `json:"cache_creation_input_token_cost_priority"`
	CacheCreationInputTokenCostAbove1hr        float64 `json:"cache_creation_input_token_cost_above_1hr"`
	CacheReadInputTokenCost                    float64 `json:"cache_read_input_token_cost"`
	CacheReadInputTokenCostAbove272KTokens     float64 `json:"cache_read_input_token_cost_above_272k_tokens"`
	CacheReadInputTokenCostPriority            float64 `json:"cache_read_input_token_cost_priority"`
	LongContextInputTokenThreshold             int     `json:"long_context_input_token_threshold,omitempty"`
	LongContextInputCostMultiplier             float64 `json:"long_context_input_cost_multiplier,omitempty"`
	LongContextOutputCostMultiplier            float64 `json:"long_context_output_cost_multiplier,omitempty"`
	SupportsServiceTier                        bool    `json:"supports_service_tier"`
	LiteLLMProvider                            string  `json:"litellm_provider"`
	Mode                                       string  `json:"mode"`
	SupportsPromptCaching                      bool    `json:"supports_prompt_caching"`
	OutputCostPerImage                         float64 `json:"output_cost_per_image"`       // 图片生成模型每张图片价格
	OutputCostPerImageToken                    float64 `json:"output_cost_per_image_token"` // 图片输出 token 价格
	InputCostPerImageToken                     float64 `json:"input_cost_per_image_token"`  // 图片输入 token 价格
	CacheReadInputImageTokenCost               float64 `json:"cache_read_input_image_token_cost"`
}

// PricingRemoteClient 远程价格数据获取接口
type PricingRemoteClient interface {
	FetchPricingJSON(ctx context.Context, url string) ([]byte, error)
	FetchHashText(ctx context.Context, url string) (string, error)
}

// ErrPricingRemoteProxySetup marks a configuration failure that must not be retried.
var ErrPricingRemoteProxySetup = errors.New("proxy client init failed and direct fallback is disabled; set security.proxy_fallback.allow_direct_on_error=true to allow fallback")

// PricingRemoteHTTPStatusError preserves the remote status for retry classification.
type PricingRemoteHTTPStatusError struct {
	StatusCode int
}

func (e *PricingRemoteHTTPStatusError) Error() string {
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

// LiteLLMRawEntry 用于解析原始JSON数据
type LiteLLMRawEntry struct {
	InputCostPerToken                          *float64 `json:"input_cost_per_token"`
	InputCostPerTokenAbove272KTokens           *float64 `json:"input_cost_per_token_above_272k_tokens"`
	InputCostPerTokenPriority                  *float64 `json:"input_cost_per_token_priority"`
	OutputCostPerToken                         *float64 `json:"output_cost_per_token"`
	OutputCostPerTokenAbove272KTokens          *float64 `json:"output_cost_per_token_above_272k_tokens"`
	OutputCostPerTokenPriority                 *float64 `json:"output_cost_per_token_priority"`
	CacheCreationInputTokenCost                *float64 `json:"cache_creation_input_token_cost"`
	CacheCreationInputTokenCostAbove272KTokens *float64 `json:"cache_creation_input_token_cost_above_272k_tokens"`
	CacheCreationInputTokenCostPriority        *float64 `json:"cache_creation_input_token_cost_priority"`
	CacheCreationInputTokenCostAbove1hr        *float64 `json:"cache_creation_input_token_cost_above_1hr"`
	CacheReadInputTokenCost                    *float64 `json:"cache_read_input_token_cost"`
	CacheReadInputTokenCostAbove272KTokens     *float64 `json:"cache_read_input_token_cost_above_272k_tokens"`
	CacheReadInputTokenCostPriority            *float64 `json:"cache_read_input_token_cost_priority"`
	LongContextInputTokenThreshold             *int     `json:"long_context_input_token_threshold"`
	LongContextInputCostMultiplier             *float64 `json:"long_context_input_cost_multiplier"`
	LongContextOutputCostMultiplier            *float64 `json:"long_context_output_cost_multiplier"`
	SupportsServiceTier                        bool     `json:"supports_service_tier"`
	LiteLLMProvider                            string   `json:"litellm_provider"`
	Mode                                       string   `json:"mode"`
	SupportsPromptCaching                      bool     `json:"supports_prompt_caching"`
	OutputCostPerImage                         *float64 `json:"output_cost_per_image"`
	OutputCostPerImageToken                    *float64 `json:"output_cost_per_image_token"`
	InputCostPerImageToken                     *float64 `json:"input_cost_per_image_token"`
	CacheReadInputImageTokenCost               *float64 `json:"cache_read_input_image_token_cost"`
}

// PricingService 动态价格服务
type PricingService struct {
	cfg          *config.Config
	remoteClient PricingRemoteClient
	mu           sync.RWMutex
	pricingData  map[string]*LiteLLMModelPricing
	pricingKeys  []string // pricingData 键的有序副本（sortedPricingKeys），只能经 setPricingDataLocked 与 pricingData 一起替换
	lastUpdated  time.Time
	localHash    string

	// 停止信号
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewPricingService 创建价格服务
func NewPricingService(cfg *config.Config, remoteClient PricingRemoteClient) *PricingService {
	s := &PricingService{
		cfg:          cfg,
		remoteClient: remoteClient,
		pricingData:  make(map[string]*LiteLLMModelPricing),
		stopCh:       make(chan struct{}),
	}
	return s
}

// Initialize 初始化价格服务
func (s *PricingService) Initialize() error {
	// 确保数据目录存在
	if err := os.MkdirAll(s.cfg.Pricing.DataDir, 0755); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Failed to create data directory: %v", err)
	}

	// 首次加载价格数据
	if err := s.checkAndUpdatePricing(); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Initial load failed, using fallback: %v", err)
		if err := s.useFallbackPricing(); err != nil {
			return fmt.Errorf("failed to load pricing data: %w", err)
		}
	}

	// 启动定时更新
	s.startUpdateScheduler()

	logger.LegacyPrintf("service.pricing", "[Pricing] Service initialized with %d models", len(s.pricingData))
	return nil
}

// Stop 停止价格服务
func (s *PricingService) Stop() {
	close(s.stopCh)
	s.wg.Wait()
	logger.LegacyPrintf("service.pricing", "%s", "[Pricing] Service stopped")
}

// startUpdateScheduler 启动定时更新调度器
func (s *PricingService) startUpdateScheduler() {
	if s == nil || s.cfg == nil || strings.TrimSpace(s.cfg.Pricing.RemoteURL) == "" {
		logger.LegacyPrintf("service.pricing", "%s", "[Pricing] Remote sync disabled: pricing remote URL is empty")
		return
	}

	// 定期检查哈希更新
	hashInterval := time.Duration(s.cfg.Pricing.HashCheckIntervalMinutes) * time.Minute
	if hashInterval < time.Minute {
		hashInterval = 10 * time.Minute
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(hashInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := s.syncWithRemote(); err != nil {
					logger.LegacyPrintf("service.pricing", "[Pricing] Sync failed: %v", err)
				}
			case <-s.stopCh:
				return
			}
		}
	}()

	logger.LegacyPrintf("service.pricing", "[Pricing] Update scheduler started (check every %v)", hashInterval)
}

// checkAndUpdatePricing 检查并更新价格数据
func (s *PricingService) checkAndUpdatePricing() error {
	pricingFile := s.getPricingFilePath()

	// 检查本地文件是否存在
	if _, err := os.Stat(pricingFile); os.IsNotExist(err) {
		logger.LegacyPrintf("service.pricing", "%s", "[Pricing] Local pricing file not found, downloading...")
		return s.downloadPricingData()
	}

	// 先加载本地文件（确保服务可用），再检查是否需要更新
	if err := s.loadPricingData(pricingFile); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Failed to load local file, downloading: %v", err)
		return s.downloadPricingData()
	}

	// 如果配置了哈希URL，通过远程哈希检查是否有更新
	if s.cfg.Pricing.HashURL != "" {
		remoteHash, err := s.fetchRemoteHash()
		if err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] Failed to fetch remote hash on startup: %v", err)
			return nil // 已加载本地文件，哈希获取失败不影响启动
		}

		s.mu.RLock()
		localHash := s.localHash
		s.mu.RUnlock()

		if localHash == "" || remoteHash != localHash {
			logger.LegacyPrintf("service.pricing", "[Pricing] Remote hash differs on startup (local=%s remote=%s), downloading...",
				localHash[:min(8, len(localHash))], remoteHash[:min(8, len(remoteHash))])
			if err := s.downloadPricingData(); err != nil {
				logger.LegacyPrintf("service.pricing", "[Pricing] Download failed, using existing file: %v", err)
			}
		}
		return nil
	}

	// 没有哈希URL时，基于文件年龄检查
	info, err := os.Stat(pricingFile)
	if err != nil {
		return nil // 已加载本地文件
	}

	fileAge := time.Since(info.ModTime())
	maxAge := time.Duration(s.cfg.Pricing.UpdateIntervalHours) * time.Hour

	if fileAge > maxAge {
		logger.LegacyPrintf("service.pricing", "[Pricing] Local file is %v old, updating...", fileAge.Round(time.Hour))
		if err := s.downloadPricingData(); err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] Download failed, using existing file: %v", err)
		}
	}

	return nil
}

// syncWithRemote 与远程同步（基于哈希校验）
func (s *PricingService) syncWithRemote() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-s.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	// 如果配置了哈希URL，从远程获取哈希进行比对
	if s.cfg.Pricing.HashURL != "" {
		remoteHash, err := s.fetchRemoteHashWithContext(ctx, pricingPeriodicHashBudget, nil)
		if err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] Failed to fetch remote hash: %v", err)
			return nil // 哈希获取失败不影响正常使用
		}

		s.mu.RLock()
		localHash := s.localHash
		s.mu.RUnlock()

		if localHash == "" || remoteHash != localHash {
			logger.LegacyPrintf("service.pricing", "[Pricing] Remote hash differs (local=%s remote=%s), downloading new version...",
				localHash[:min(8, len(localHash))], remoteHash[:min(8, len(remoteHash))])
			return s.downloadPricingDataWithParentContext(ctx)
		}
		logger.LegacyPrintf("service.pricing", "%s", "[Pricing] Hash check passed, no update needed")
		return nil
	}

	// 没有哈希URL时，基于时间检查
	pricingFile := s.getPricingFilePath()
	info, err := os.Stat(pricingFile)
	if err != nil {
		return s.downloadPricingDataWithParentContext(ctx)
	}

	fileAge := time.Since(info.ModTime())
	maxAge := time.Duration(s.cfg.Pricing.UpdateIntervalHours) * time.Hour

	if fileAge > maxAge {
		logger.LegacyPrintf("service.pricing", "[Pricing] File is %v old, downloading...", fileAge.Round(time.Hour))
		return s.downloadPricingDataWithParentContext(ctx)
	}

	return nil
}

// downloadPricingData 从远程下载价格数据
func (s *PricingService) downloadPricingData() error {
	return s.downloadPricingDataWithParentContext(context.Background())
}

func (s *PricingService) downloadPricingDataWithParentContext(parent context.Context) error {
	// The existing 30-second budget begins before the optional hash probe. Keep
	// the hash, catalog attempts, and retry waits inside this one deadline.
	ctx, cancel := context.WithTimeout(parent, pricingDownloadBudget)
	defer cancel()
	return s.downloadPricingDataWithContext(ctx, nil)
}

func (s *PricingService) downloadPricingDataWithContext(ctx context.Context, wait pricingRetryWaitFunc) error {
	remoteURL, err := s.validatePricingURL(s.cfg.Pricing.RemoteURL)
	if err != nil {
		return err
	}
	logger.LegacyPrintf("service.pricing", "[Pricing] Downloading from %s", remoteURL)

	// 获取远程哈希（用于同步锚点，不作为完整性校验）
	var remoteHash string
	if strings.TrimSpace(s.cfg.Pricing.HashURL) != "" {
		remoteHash, err = s.fetchRemoteHashWithContext(ctx, pricingStartupHashBudget, wait)
		if err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] Failed to fetch remote hash (continuing): %v", err)
		}
	}

	body, err := s.fetchPricingJSONWithContext(ctx, remoteURL, wait)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	// 哈希校验：不匹配时仅告警，不阻止更新
	// 远程哈希文件可能与数据文件不同步（如维护者更新了数据但未更新哈希文件）
	dataHash := sha256.Sum256(body)
	dataHashStr := hex.EncodeToString(dataHash[:])
	if remoteHash != "" && !strings.EqualFold(remoteHash, dataHashStr) {
		logger.LegacyPrintf("service.pricing", "[Pricing] Hash mismatch warning: remote=%s data=%s (hash file may be out of sync)",
			remoteHash[:min(8, len(remoteHash))], dataHashStr[:8])
	}

	// 解析JSON数据（使用灵活的解析方式）
	data, err := s.parsePricingData(body)
	if err != nil {
		return fmt.Errorf("parse pricing data: %w", err)
	}

	// 保存到本地文件
	pricingFile := s.getPricingFilePath()
	if err := os.WriteFile(pricingFile, body, 0644); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Failed to save file: %v", err)
	}

	// 使用远程哈希作为同步锚点，防止重复下载
	// 当远程哈希不可用时，回退到数据本身的哈希
	syncHash := dataHashStr
	if remoteHash != "" {
		syncHash = remoteHash
	}
	hashFile := s.getHashFilePath()
	if err := os.WriteFile(hashFile, []byte(syncHash+"\n"), 0644); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Failed to save hash: %v", err)
	}

	// 更新内存数据
	s.mu.Lock()
	s.setPricingDataLocked(data)
	s.lastUpdated = time.Now()
	s.localHash = syncHash
	s.mu.Unlock()

	logger.LegacyPrintf("service.pricing", "[Pricing] Downloaded %d models successfully", len(data))
	return nil
}

// parsePricingData 解析价格数据（处理各种格式）
func (s *PricingService) parsePricingData(body []byte) (map[string]*LiteLLMModelPricing, error) {
	// 首先解析为 map[string]json.RawMessage
	var rawData map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawData); err != nil {
		return nil, fmt.Errorf("parse raw JSON: %w", err)
	}

	result := make(map[string]*LiteLLMModelPricing)
	skipped := 0

	for modelName, rawEntry := range rawData {
		// 跳过 sample_spec 等文档条目
		if modelName == "sample_spec" {
			continue
		}

		// 尝试解析每个条目
		var entry LiteLLMRawEntry
		if err := json.Unmarshal(rawEntry, &entry); err != nil {
			skipped++
			continue
		}

		// 只保留有有效价格的条目
		if entry.InputCostPerToken == nil && entry.OutputCostPerToken == nil &&
			entry.InputCostPerImageToken == nil && entry.OutputCostPerImageToken == nil && entry.OutputCostPerImage == nil {
			continue
		}

		pricing := &LiteLLMModelPricing{
			LiteLLMProvider:       entry.LiteLLMProvider,
			Mode:                  entry.Mode,
			SupportsPromptCaching: entry.SupportsPromptCaching,
			SupportsServiceTier:   entry.SupportsServiceTier,
		}

		if entry.InputCostPerToken != nil {
			pricing.InputCostPerToken = *entry.InputCostPerToken
		}
		if entry.InputCostPerTokenAbove272KTokens != nil {
			pricing.InputCostPerTokenAbove272KTokens = *entry.InputCostPerTokenAbove272KTokens
		}
		if entry.InputCostPerTokenPriority != nil {
			pricing.InputCostPerTokenPriority = *entry.InputCostPerTokenPriority
		}
		if entry.OutputCostPerToken != nil {
			pricing.OutputCostPerToken = *entry.OutputCostPerToken
		}
		if entry.OutputCostPerTokenAbove272KTokens != nil {
			pricing.OutputCostPerTokenAbove272KTokens = *entry.OutputCostPerTokenAbove272KTokens
		}
		if entry.OutputCostPerTokenPriority != nil {
			pricing.OutputCostPerTokenPriority = *entry.OutputCostPerTokenPriority
		}
		if entry.CacheCreationInputTokenCost != nil {
			pricing.CacheCreationInputTokenCost = *entry.CacheCreationInputTokenCost
		}
		if entry.CacheCreationInputTokenCostAbove272KTokens != nil {
			pricing.CacheCreationInputTokenCostAbove272KTokens = *entry.CacheCreationInputTokenCostAbove272KTokens
		}
		if entry.CacheCreationInputTokenCostPriority != nil {
			pricing.CacheCreationInputTokenCostPriority = *entry.CacheCreationInputTokenCostPriority
		}
		if entry.CacheCreationInputTokenCostAbove1hr != nil {
			pricing.CacheCreationInputTokenCostAbove1hr = *entry.CacheCreationInputTokenCostAbove1hr
		}
		if entry.CacheReadInputTokenCost != nil {
			pricing.CacheReadInputTokenCost = *entry.CacheReadInputTokenCost
		}
		if entry.CacheReadInputTokenCostAbove272KTokens != nil {
			pricing.CacheReadInputTokenCostAbove272KTokens = *entry.CacheReadInputTokenCostAbove272KTokens
		}
		if entry.CacheReadInputTokenCostPriority != nil {
			pricing.CacheReadInputTokenCostPriority = *entry.CacheReadInputTokenCostPriority
		}
		if entry.LongContextInputTokenThreshold != nil {
			pricing.LongContextInputTokenThreshold = *entry.LongContextInputTokenThreshold
		}
		if entry.LongContextInputCostMultiplier != nil {
			pricing.LongContextInputCostMultiplier = *entry.LongContextInputCostMultiplier
		}
		if entry.LongContextOutputCostMultiplier != nil {
			pricing.LongContextOutputCostMultiplier = *entry.LongContextOutputCostMultiplier
		}
		if entry.OutputCostPerImage != nil {
			pricing.OutputCostPerImage = *entry.OutputCostPerImage
		}
		if entry.OutputCostPerImageToken != nil {
			pricing.OutputCostPerImageToken = *entry.OutputCostPerImageToken
		}
		if entry.InputCostPerImageToken != nil {
			pricing.InputCostPerImageToken = *entry.InputCostPerImageToken
		}
		if entry.CacheReadInputImageTokenCost != nil {
			pricing.CacheReadInputImageTokenCost = *entry.CacheReadInputImageTokenCost
		}

		result[modelName] = pricing
	}

	if skipped > 0 {
		logger.LegacyPrintf("service.pricing", "[Pricing] Skipped %d invalid entries", skipped)
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("no valid pricing entries found")
	}

	return result, nil
}

// loadPricingData 从本地文件加载价格数据
func (s *PricingService) loadPricingData(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file failed: %w", err)
	}

	// 使用灵活的解析方式
	pricingData, err := s.parsePricingData(data)
	if err != nil {
		return fmt.Errorf("parse pricing data: %w", err)
	}

	// 计算哈希
	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])

	s.mu.Lock()
	s.setPricingDataLocked(pricingData)
	s.localHash = hashStr

	info, _ := os.Stat(filePath)
	if info != nil {
		s.lastUpdated = info.ModTime()
	} else {
		s.lastUpdated = time.Now()
	}
	s.mu.Unlock()

	logger.LegacyPrintf("service.pricing", "[Pricing] Loaded %d models from %s", len(pricingData), filePath)
	return nil
}

// useFallbackPricing 使用回退价格文件
func (s *PricingService) useFallbackPricing() error {
	fallbackFile := s.cfg.Pricing.FallbackFile

	if _, err := os.Stat(fallbackFile); os.IsNotExist(err) {
		return fmt.Errorf("fallback file not found: %s", fallbackFile)
	}

	logger.LegacyPrintf("service.pricing", "[Pricing] Using fallback file: %s", fallbackFile)

	// 复制到数据目录
	data, err := os.ReadFile(fallbackFile)
	if err != nil {
		return fmt.Errorf("read fallback failed: %w", err)
	}

	pricingFile := s.getPricingFilePath()
	if err := os.WriteFile(pricingFile, data, 0644); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Failed to copy fallback: %v", err)
	}

	return s.loadPricingData(fallbackFile)
}

// fetchRemoteHash 从远程获取哈希值
func (s *PricingService) fetchRemoteHash() (string, error) {
	return s.fetchRemoteHashWithContext(context.Background(), pricingStartupHashBudget, nil)
}

const (
	pricingRemoteAttempts     = 3
	pricingRetryBaseBackoff   = 250 * time.Millisecond
	pricingStartupHashBudget  = 10 * time.Second
	pricingPeriodicHashBudget = 25 * time.Second
	pricingDownloadBudget     = 30 * time.Second
)

type pricingRetryWaitFunc func(context.Context, time.Duration) error

func waitForPricingRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isTransientPricingRemoteError(err error) bool {
	if err == nil || errors.Is(err, ErrPricingRemoteProxySetup) || errors.Is(err, context.Canceled) {
		return false
	}
	var statusErr *PricingRemoteHTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode == 408 || statusErr.StatusCode == 429 || statusErr.StatusCode >= 500
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func retryPricingRemote(ctx context.Context, label string, wait pricingRetryWaitFunc, op func(context.Context) error) (int, error) {
	if wait == nil {
		wait = waitForPricingRetry
	}
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return attempt - 1, err
		}
		err := op(ctx)
		if err == nil {
			return attempt, nil
		}
		if !isTransientPricingRemoteError(err) || attempt == pricingRemoteAttempts || ctx.Err() != nil {
			return attempt, err
		}
		delay := pricingRetryBaseBackoff * time.Duration(1<<(attempt-1))
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay {
			return attempt, err
		}
		logger.LegacyPrintf("service.pricing", "[Pricing] %s attempt %d/%d failed, retrying in %v: %v",
			label, attempt, pricingRemoteAttempts, delay, err)
		if waitErr := wait(ctx, delay); waitErr != nil {
			return attempt, waitErr
		}
	}
}

func (s *PricingService) fetchRemoteHashWithContext(parent context.Context, budget time.Duration, wait pricingRetryWaitFunc) (string, error) {
	hashURL, err := s.validatePricingURL(s.cfg.Pricing.HashURL)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()

	var hash string
	attempts, err := retryPricingRemote(ctx, "remote hash fetch", wait, func(ctx context.Context) error {
		value, fetchErr := s.remoteClient.FetchHashText(ctx, hashURL)
		if fetchErr == nil {
			hash = value
		}
		return fetchErr
	})
	if err != nil {
		return "", fmt.Errorf("remote hash fetch stopped after %d attempt(s): %w", attempts, err)
	}
	return strings.TrimSpace(hash), nil
}

func (s *PricingService) fetchPricingJSONWithContext(ctx context.Context, remoteURL string, wait pricingRetryWaitFunc) ([]byte, error) {
	var body []byte
	attempts, err := retryPricingRemote(ctx, "pricing download", wait, func(ctx context.Context) error {
		value, fetchErr := s.remoteClient.FetchPricingJSON(ctx, remoteURL)
		if fetchErr == nil {
			body = value
		}
		return fetchErr
	})
	if err != nil {
		return nil, fmt.Errorf("pricing download stopped after %d attempt(s): %w", attempts, err)
	}
	return body, nil
}

func (s *PricingService) validatePricingURL(raw string) (string, error) {
	if s.cfg != nil && !s.cfg.Security.URLAllowlist.Enabled {
		normalized, err := urlvalidator.ValidateURLFormat(raw, s.cfg.Security.URLAllowlist.AllowInsecureHTTP)
		if err != nil {
			return "", fmt.Errorf("invalid pricing url: %w", err)
		}
		return normalized, nil
	}
	normalized, err := urlvalidator.ValidateHTTPSURL(raw, urlvalidator.ValidationOptions{
		AllowedHosts:     s.cfg.Security.URLAllowlist.PricingHosts,
		RequireAllowlist: true,
		AllowPrivate:     s.cfg.Security.URLAllowlist.AllowPrivateHosts,
	})
	if err != nil {
		return "", fmt.Errorf("invalid pricing url: %w", err)
	}
	return normalized, nil
}

// pricingKeyScopePattern 匹配带厂商或区域前缀的键，如 us.anthropic.claude-...、anthropic.claude-...。
// 目录里的版本号小数点（gpt-5.2、claude-opus-4.5、kimi-k2.5）前面是字母数字和连字符、后面是数字，不会被误判。
var pricingKeyScopePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*\.[a-z]`)

// isScopedPricingKey 判断键是否带厂商或区域前缀（bedrock/…、vertex_ai/…、us.anthropic.… 这类）。
// 这类键的价格可能是区域价，模糊匹配时排在无前缀键之后。
func isScopedPricingKey(key string) bool {
	return strings.Contains(key, "/") || pricingKeyScopePattern.MatchString(strings.ToLower(key))
}

// sortedPricingKeys 返回价格目录全部键的确定顺序，供两处模糊匹配（GetModelPricing 第 3 步的
// 基名匹配、matchByModelFamily 第 3 阶段的系列匹配）遍历，使多个键同时命中时的结果不再
// 取决于 Go map 的随机遍历顺序。顺序依次为：
//  1. 不带 "/"、不带厂商或区域前缀的键在前；
//  2. 键更短的在前；
//  3. 字节序字典序。
func sortedPricingKeys(data map[string]*LiteLLMModelPricing) []string {
	type rankedKey struct {
		key    string
		scoped bool
	}
	ranked := make([]rankedKey, 0, len(data))
	for key := range data {
		ranked = append(ranked, rankedKey{key: key, scoped: isScopedPricingKey(key)})
	}
	sort.Slice(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.scoped != b.scoped {
			return !a.scoped
		}
		if len(a.key) != len(b.key) {
			return len(a.key) < len(b.key)
		}
		return a.key < b.key
	})
	keys := make([]string, len(ranked))
	for i, r := range ranked {
		keys[i] = r.key
	}
	return keys
}

// setPricingDataLocked 替换价格目录，同时重建排好序的键切片，保证两者永远成对出现
// （热更新时不会出现新 map 配旧切片）。调用方必须持有 s.mu 的写锁。
func (s *PricingService) setPricingDataLocked(data map[string]*LiteLLMModelPricing) {
	s.pricingData = data
	s.pricingKeys = sortedPricingKeys(data)
}

// pricingKeyOrderLocked 返回模糊匹配使用的键顺序，调用方必须持有 s.mu（读锁即可）。
// 正式路径下 pricingKeys 由 setPricingDataLocked 与 pricingData 一起维护；不经加载函数、
// 直接给 pricingData 赋值的构造方式（主要是测试）键数对不上，此时现算一份，顺序规则相同。
func (s *PricingService) pricingKeyOrderLocked() []string {
	if len(s.pricingKeys) == len(s.pricingData) {
		return s.pricingKeys
	}
	return sortedPricingKeys(s.pricingData)
}

// GetModelPricing 获取模型价格（带模糊匹配）
func (s *PricingService) GetModelPricing(modelName string) (result *LiteLLMModelPricing) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	defer func() {
		result = applyGPT56CacheWriteFallback(modelName, result)
		result = s.applyGPT56SolPromotionalPricing(modelName, result)
	}()

	if modelName == "" {
		return nil
	}

	// 标准化模型名称（同时兼容 "models/xxx"、VertexAI 资源名等前缀）
	modelLower := strings.ToLower(strings.TrimSpace(modelName))
	lookupCandidates := s.buildModelLookupCandidates(modelLower)

	// 1. 精确匹配
	for _, candidate := range lookupCandidates {
		if candidate == "" {
			continue
		}
		if pricing, ok := s.pricingData[candidate]; ok {
			return pricing
		}
	}

	// 2. 处理常见的模型名称变体
	// claude-opus-4-5-20251101 -> claude-opus-4.5-20251101
	for _, candidate := range lookupCandidates {
		normalized := strings.ReplaceAll(candidate, "-4-5-", "-4.5-")
		if pricing, ok := s.pricingData[normalized]; ok {
			return pricing
		}
	}

	// 3. 尝试模糊匹配（去掉版本号后缀）
	// claude-opus-4-5-20251101 -> claude-opus-4.5
	// 多个键基名相同时，键名恰好等于基名的优先，其余按 sortedPricingKeys 的顺序取第一个，
	// 结果与 map 遍历顺序无关。
	baseName := s.extractBaseName(lookupCandidates[0])
	baseMatchKey := ""
	for _, key := range s.pricingKeyOrderLocked() {
		keyLower := strings.ToLower(key)
		if s.extractBaseName(keyLower) != baseName {
			continue
		}
		if keyLower == baseName {
			baseMatchKey = key
			break
		}
		if baseMatchKey == "" {
			baseMatchKey = key
		}
	}
	if baseMatchKey != "" {
		return s.pricingData[baseMatchKey]
	}

	// 4. 基于模型系列匹配（Claude）
	if pricing := s.matchByModelFamily(lookupCandidates[0]); pricing != nil {
		return pricing
	}

	// 5. OpenAI 模型回退策略
	if strings.HasPrefix(lookupCandidates[0], "gpt-") {
		return s.matchOpenAIModel(lookupCandidates[0])
	}

	return nil
}

const defaultPricingRemoteURL = "https://raw.githubusercontent.com/Wei-Shaw/model-price-repo/main/model_prices_and_context_window.json"

func (s *PricingService) usesDefaultPricingCatalog() bool {
	if s == nil || s.cfg == nil {
		return true
	}
	remoteURL := strings.TrimSpace(s.cfg.Pricing.RemoteURL)
	return remoteURL == "" || remoteURL == defaultPricingRemoteURL
}

func isOpenAIGPT56SolModel(model string) bool {
	return normalizeKnownOpenAICodexModel(model) == "gpt-5.6-sol"
}

// The default remote catalog can lag the official Sol promotion and is loaded
// ahead of the bundled and hardcoded fallbacks. Correct only its known old rate
// card; a configured third-party catalog remains operator-controlled. This is
// a rate review item for 2026-11-21, not an automatic expiration on that date.
func (s *PricingService) applyGPT56SolPromotionalPricing(model string, pricing *LiteLLMModelPricing) *LiteLLMModelPricing {
	if pricing == nil || !isOpenAIGPT56SolModel(model) || !s.usesDefaultPricingCatalog() {
		return pricing
	}
	if pricing.InputCostPerToken != 5e-6 || pricing.CacheReadInputTokenCost != 0.5e-6 || pricing.OutputCostPerToken != 30e-6 {
		return pricing
	}
	cloned := *pricing
	cloned.InputCostPerToken = 4e-6
	cloned.InputCostPerTokenAbove272KTokens = 8e-6
	cloned.InputCostPerTokenPriority = 8e-6
	cloned.OutputCostPerToken = 20e-6
	cloned.OutputCostPerTokenAbove272KTokens = 30e-6
	cloned.OutputCostPerTokenPriority = 40e-6
	cloned.CacheCreationInputTokenCost = 5e-6
	cloned.CacheCreationInputTokenCostAbove272KTokens = 10e-6
	cloned.CacheCreationInputTokenCostPriority = 10e-6
	cloned.CacheReadInputTokenCost = 0.4e-6
	cloned.CacheReadInputTokenCostAbove272KTokens = 0.8e-6
	cloned.CacheReadInputTokenCostPriority = 0.8e-6
	return &cloned
}

// applyGPT56CacheWriteFallback keeps stale local catalogs billable until their
// next successful refresh. GPT-5.6's official long-context policy remains in
// force even when a catalog omits its multiplier metadata. See
// docs/specs/gpt-5-6-272k-pricing-policy.md §2.
func applyGPT56CacheWriteFallback(model string, pricing *LiteLLMModelPricing) *LiteLLMModelPricing {
	if pricing == nil || !isOpenAIGPT56Model(model) {
		return pricing
	}

	needsBase := pricing.CacheCreationInputTokenCost <= 0 && pricing.InputCostPerToken > 0
	needsPriority := pricing.CacheCreationInputTokenCostPriority <= 0 && pricing.InputCostPerTokenPriority > 0
	needsLongContextPolicy := pricing.LongContextInputTokenThreshold != gpt56LongContextTokenThreshold ||
		pricing.LongContextInputCostMultiplier != gpt56LongContextInputMultiplier ||
		pricing.LongContextOutputCostMultiplier != gpt56LongContextOutputMultiplier
	if !needsBase && !needsPriority && !needsLongContextPolicy {
		return pricing
	}

	cloned := *pricing
	if needsBase {
		cloned.CacheCreationInputTokenCost = cloned.InputCostPerToken * 1.25
	}
	if needsPriority {
		cloned.CacheCreationInputTokenCostPriority = cloned.InputCostPerTokenPriority * 1.25
	}
	cloned.LongContextInputTokenThreshold = gpt56LongContextTokenThreshold
	cloned.LongContextInputCostMultiplier = gpt56LongContextInputMultiplier
	cloned.LongContextOutputCostMultiplier = gpt56LongContextOutputMultiplier
	return &cloned
}

func (s *PricingService) buildModelLookupCandidates(modelLower string) []string {
	rawCandidates := []string{
		modelLower,
		strings.TrimPrefix(modelLower, "models/"),
		lastSegment(modelLower),
		lastSegment(strings.TrimPrefix(modelLower, "models/")),
	}
	normalized := normalizeModelNameForPricing(modelLower)

	// A tier-specific entry should take precedence when the pricing catalog gains
	// one later. Antigravity's Gemini Flash thinking tiers share the base rate,
	// so the normalized base remains the fallback after the exact aliases.
	candidates := rawCandidates
	if normalizeGeminiThinkingTierAlias(lastSegment(modelLower)) != lastSegment(modelLower) {
		candidates = append(candidates, normalized)
	} else {
		// Prefer canonical model names for all other aliases (including models/xxx).
		candidates = append([]string{normalized}, candidates...)
	}

	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	if len(out) == 0 {
		return []string{modelLower}
	}
	return out
}

func normalizeModelNameForPricing(model string) string {
	// Common Gemini/VertexAI forms:
	// - models/gemini-2.0-flash-exp
	// - publishers/google/models/gemini-2.5-pro
	// - projects/.../locations/.../publishers/google/models/gemini-2.5-pro
	model = strings.TrimSpace(model)
	model = strings.TrimLeft(model, "/")
	model = strings.TrimPrefix(model, "models/")
	model = strings.TrimPrefix(model, "publishers/google/models/")

	if idx := strings.LastIndex(model, "/publishers/google/models/"); idx != -1 {
		model = model[idx+len("/publishers/google/models/"):]
	}
	if idx := strings.LastIndex(model, "/models/"); idx != -1 {
		model = model[idx+len("/models/"):]
	}

	model = strings.TrimLeft(model, "/")
	if canonical := canonicalizeOpenAIModelAliasSpelling(model); canonical != "" {
		if canonical == "gpt-6" {
			return "gpt-6-astra"
		}
		// gpt-6-sol-max / gpt-6-luna-openai-compact / gpt-6.1-sol-max 等本地后缀写法归一到官方 ID，
		// 让目录里的 gpt-6-sol / gpt-6-luna / gpt-6.1-sol 条目（含自定义覆盖）优先命中。
		if openai.IsGPT6SolOrLunaModelSpelling(canonical) || openai.IsGPT61SolModelSpelling(canonical) {
			return normalizeKnownOpenAICodexModel(canonical)
		}
		// Mirror normalizeKnownOpenAICodexModel's bare "gpt-5.6" -> "gpt-5.6-sol"
		// redirect so pricing lookups hit the dynamic pricing source instead of
		// silently falling back to the static Go fallback table.
		if canonical == "gpt-5.6" {
			return "gpt-5.6-sol"
		}
		return canonical
	}
	return normalizeGeminiThinkingTierAlias(model)
}

// normalizeGeminiThinkingTierAlias maps Antigravity's Gemini Flash
// thinking-tier model IDs to the public base model. The tier controls reasoning
// behavior, not the published token rate, so this keeps -high/-low/-medium and
// -tiered requests on the corresponding base model's price card.
func normalizeGeminiThinkingTierAlias(model string) string {
	for _, baseModel := range []string{"gemini-3.6-flash", "gemini-3.7-flash", "gemini-3.8-flash"} {
		for _, tier := range []string{"-high", "-low", "-medium", "-tiered"} {
			if model == baseModel+tier {
				return baseModel
			}
		}
	}
	return model
}

func lastSegment(model string) string {
	if idx := strings.LastIndex(model, "/"); idx != -1 {
		return model[idx+1:]
	}
	return model
}

// extractBaseName 提取基础模型名称（去掉日期版本号）
func (s *PricingService) extractBaseName(model string) string {
	// 移除日期后缀 (如 -20251101, -20241022)
	parts := strings.Split(model, "-")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		// 跳过看起来像日期的部分（8位数字）
		if len(part) == 8 && isNumeric(part) {
			continue
		}
		// 跳过版本号（如 v1:0）
		if strings.Contains(part, ":") {
			continue
		}
		result = append(result, part)
	}
	return strings.Join(result, "-")
}

// matchByModelFamily 基于模型系列匹配
func (s *PricingService) matchByModelFamily(model string) *LiteLLMModelPricing {
	// Opus 5.5 有独立价格，不能被下面 "claude-opus-5" 的子串匹配归到 Opus 5。
	if claude.IsOpus55(model) {
		if pricing, ok := s.pricingData[claude.Opus55ModelID]; ok {
			return pricing
		}
		return claudeOpus55FallbackPricing
	}
	// modelFamily 定义一个模型系列的匹配和定价查找规则。
	type modelFamily struct {
		name    string   // 系列名称
		match   []string // 用于将模型归类到此系列的模式（strings.Contains 匹配）
		pricing []string // 用于在定价数据中查找价格的模式（nil 则复用 match；可包含低版本 fallback）
	}

	// 按特异性降序排列：高版本号在前，避免 "claude-opus-4"（opus-4 系列）
	// 因子串关系误匹配 "claude-opus-4-7"（opus-4.7 系列）。
	// 注意：原 map 实现存在 Go map 迭代随机性导致的同类 bug，此处改为有序切片修复。
	families := []modelFamily{
		// Opus 5 and Opus 4.8 have the same rate. If the dynamic pricing data is
		// missing Opus 5, do not fall through to the older $15/$75 Opus family.
		{name: "opus-5", match: []string{"claude-opus-5"}, pricing: []string{"claude-opus-5", "claude-opus-4-8"}},
		{name: "opus-4.8", match: []string{"claude-opus-4-8", "claude-opus-4.8"}, pricing: []string{"claude-opus-4-8", "claude-opus-4.8", "claude-opus-4-7"}},
		{name: "opus-4.7", match: []string{"claude-opus-4-7", "claude-opus-4.7"}, pricing: []string{"claude-opus-4-7", "claude-opus-4.7", "claude-opus-4-6"}},
		{name: "opus-4.6", match: []string{"claude-opus-4-6", "claude-opus-4.6"}},
		{name: "opus-4.5", match: []string{"claude-opus-4-5", "claude-opus-4.5"}},
		{name: "opus-4", match: []string{"claude-opus-4", "claude-3-opus"}},
		{name: "sonnet-4.5", match: []string{"claude-sonnet-4-5", "claude-sonnet-4.5"}},
		{name: "sonnet-4", match: []string{"claude-sonnet-4", "claude-3-5-sonnet"}},
		{name: "sonnet-3.5", match: []string{"claude-3-5-sonnet", "claude-3.5-sonnet"}},
		{name: "sonnet-3", match: []string{"claude-3-sonnet"}},
		{name: "haiku-3.5", match: []string{"claude-3-5-haiku", "claude-3.5-haiku"}},
		{name: "haiku-3", match: []string{"claude-3-haiku"}},
	}

	// Phase 1: 按有序切片归类（最具体的系列优先匹配）
	var matched *modelFamily
	for i := range families {
		for _, pattern := range families[i].match {
			if strings.Contains(model, pattern) || strings.Contains(model, strings.ReplaceAll(pattern, "-", "")) {
				matched = &families[i]
				break
			}
		}
		if matched != nil {
			break
		}
	}

	// Phase 2: 二次兜底——当模型 ID 不含已知模式串时，按关键字粗分
	if matched == nil {
		var fallbackName string
		switch {
		case strings.Contains(model, "opus"):
			switch {
			case strings.Contains(model, "opus-5") || strings.Contains(model, "opus5"):
				fallbackName = "opus-5"
			case strings.Contains(model, "4.8") || strings.Contains(model, "4-8"):
				fallbackName = "opus-4.8"
			case strings.Contains(model, "4.7") || strings.Contains(model, "4-7"):
				fallbackName = "opus-4.7"
			case strings.Contains(model, "4.6") || strings.Contains(model, "4-6"):
				fallbackName = "opus-4.6"
			case strings.Contains(model, "4.5") || strings.Contains(model, "4-5"):
				fallbackName = "opus-4.5"
			default:
				fallbackName = "opus-4"
			}
		case strings.Contains(model, "sonnet"):
			switch {
			case strings.Contains(model, "4.5") || strings.Contains(model, "4-5"):
				fallbackName = "sonnet-4.5"
			case strings.Contains(model, "3-5") || strings.Contains(model, "3.5"):
				fallbackName = "sonnet-3.5"
			default:
				fallbackName = "sonnet-4"
			}
		case strings.Contains(model, "haiku"):
			switch {
			case strings.Contains(model, "3-5") || strings.Contains(model, "3.5"):
				fallbackName = "haiku-3.5"
			default:
				fallbackName = "haiku-3"
			}
		}
		if fallbackName != "" {
			for i := range families {
				if families[i].name == fallbackName {
					matched = &families[i]
					break
				}
			}
		}
	}

	if matched == nil {
		return nil
	}

	// Phase 3: 在定价数据中查找该系列的价格
	lookups := matched.pricing
	if lookups == nil {
		lookups = matched.match
	}
	// 同一个 pattern 命中多个键时按 sortedPricingKeys 的顺序取第一个，结果与 map 遍历顺序无关。
	keys := s.pricingKeyOrderLocked()
	for _, pattern := range lookups {
		for _, key := range keys {
			keyLower := strings.ToLower(key)
			if matched.name == "opus-5" && claude.IsOpus55(keyLower) {
				continue
			}
			if strings.Contains(keyLower, pattern) {
				logger.LegacyPrintf("service.pricing", "[Pricing] Fuzzy matched %s -> %s", model, key)
				return s.pricingData[key]
			}
		}
	}

	return nil
}

// matchOpenAIModel OpenAI 模型回退匹配策略
// 回退顺序：
// 1. gpt-5.3-codex-spark* -> gpt-5.1-codex（按业务要求固定计费）
// 2. gpt-5.2-codex -> gpt-5.2（去掉后缀如 -codex, -mini, -max 等）
// 3. gpt-5.2-20251222 -> gpt-5.2（去掉日期版本号）
// 4. gpt-5.3-codex -> gpt-5.2-codex
// 5. gpt-5.4* -> 业务静态兜底价
// 6. 最终回退到 DefaultTestModel (gpt-5.1-codex)
func (s *PricingService) matchOpenAIModel(model string) *LiteLLMModelPricing {
	if strings.HasPrefix(model, "gpt-5.3-codex-spark") {
		if pricing, ok := s.pricingData["gpt-5.1-codex"]; ok {
			logger.LegacyPrintf("service.pricing", "[Pricing][SparkBilling] %s -> %s billing", model, "gpt-5.1-codex")
			logger.With(zap.String("component", "service.pricing")).
				Info(fmt.Sprintf("[Pricing] OpenAI fallback matched %s -> %s", model, "gpt-5.1-codex"))
			return pricing
		}
	}

	// GPT-6.1 Sol / GPT-6 Sol / Luna 必须在基础版本号回退之前处理：generateOpenAIModelVariants
	// 会把 gpt-6-sol 截成 gpt-6，从而错误命中 Astra（gpt-6 别名）的价格。
	if openai.IsGPT61SolModelSpelling(model) {
		if pricing, ok := s.pricingData["gpt-6.1-sol"]; ok {
			return pricing
		}
		return openAIGPT61SolFallbackPricing
	}
	if openai.IsGPT6SolOrLunaModelSpelling(model) {
		base := normalizeKnownOpenAICodexModel(model)
		if pricing, ok := s.pricingData[base]; ok {
			return pricing
		}
		if base == "gpt-6-sol" {
			return openAIGPT6SolFallbackPricing
		}
		return openAIGPT6LunaFallbackPricing
	}

	// 尝试的回退变体
	variants := s.generateOpenAIModelVariants(model, openAIModelDatePattern)

	for _, variant := range variants {
		if pricing, ok := s.pricingData[variant]; ok {
			logger.With(zap.String("component", "service.pricing")).
				Info(fmt.Sprintf("[Pricing] OpenAI fallback matched %s -> %s", model, variant))
			return pricing
		}
	}

	if strings.HasPrefix(model, "gpt-5.3-codex") {
		if pricing, ok := s.pricingData["gpt-5.2-codex"]; ok {
			logger.With(zap.String("component", "service.pricing")).
				Info(fmt.Sprintf("[Pricing] OpenAI fallback matched %s -> %s", model, "gpt-5.2-codex"))
			return pricing
		}
	}

	// GPT-6 Astra 与 GPT-5.6 的三个 SKU 必须保留各自的离线定价，不能回退到 GPT-5.4。
	switch normalizeKnownOpenAICodexModel(model) {
	case "gpt-6-astra":
		return openAIGPT6AstraFallbackPricing
	case "gpt-5.6-sol":
		return openAIGPT56SolFallbackPricing
	case "gpt-5.6-terra":
		return openAIGPT56TerraFallbackPricing
	case "gpt-5.6-luna":
		return openAIGPT56LunaFallbackPricing
	}

	// GPT-5.5 回退到 GPT-5.4 定价
	if strings.HasPrefix(model, "gpt-5.5") {
		logger.With(zap.String("component", "service.pricing")).
			Info(fmt.Sprintf("[Pricing] OpenAI fallback matched %s -> %s", model, "gpt-5.4(static)"))
		return openAIGPT54FallbackPricing
	}

	if strings.HasPrefix(model, "gpt-5.4-mini") {
		logger.With(zap.String("component", "service.pricing")).
			Info(fmt.Sprintf("[Pricing] OpenAI fallback matched %s -> %s", model, "gpt-5.4-mini(static)"))
		return openAIGPT54MiniFallbackPricing
	}

	if strings.HasPrefix(model, "gpt-5.4-nano") {
		logger.With(zap.String("component", "service.pricing")).
			Info(fmt.Sprintf("[Pricing] OpenAI fallback matched %s -> %s", model, "gpt-5.4-nano(static)"))
		return openAIGPT54NanoFallbackPricing
	}

	if strings.HasPrefix(model, "gpt-5.4") {
		logger.With(zap.String("component", "service.pricing")).
			Info(fmt.Sprintf("[Pricing] OpenAI fallback matched %s -> %s", model, "gpt-5.4(static)"))
		return openAIGPT54FallbackPricing
	}

	// Remote price mirrors can lag new releases. Never bill GPT Image 2.5
	// using the older image model's rates when its entry is absent.
	for _, imageModel := range []string{"gpt-image-2.5-flare", "gpt-image-2.5-sunburst"} {
		if model == imageModel || model == imageModel+"-2026-09-08" {
			return openAIGPTImage25FallbackPricing
		}
	}
	if isOpenAIImageGenerationModel(model) {
		for _, candidate := range []string{"gpt-image-2", "gpt-image-1.5", "gpt-image-1"} {
			if pricing, ok := s.pricingData[candidate]; ok {
				logger.LegacyPrintf("service.pricing", "[Pricing] OpenAI image fallback matched %s -> %s", model, candidate)
				return pricing
			}
		}
		return nil
	}

	// 最终回退到 DefaultTestModel
	defaultModel := strings.ToLower(openai.DefaultTestModel)
	if pricing, ok := s.pricingData[defaultModel]; ok {
		logger.LegacyPrintf("service.pricing", "[Pricing] OpenAI fallback to default model %s -> %s", model, defaultModel)
		return pricing
	}

	return nil
}

// generateOpenAIModelVariants 生成 OpenAI 模型的回退变体列表
func (s *PricingService) generateOpenAIModelVariants(model string, datePattern *regexp.Regexp) []string {
	seen := make(map[string]bool)
	var variants []string

	addVariant := func(v string) {
		if v != model && !seen[v] {
			seen[v] = true
			variants = append(variants, v)
		}
	}

	// 1. 去掉日期版本号: gpt-5.2-20251222 -> gpt-5.2
	withoutDate := datePattern.ReplaceAllString(model, "")
	if withoutDate != model {
		addVariant(withoutDate)
	}

	// 2. 提取基础版本号: gpt-5.2-codex -> gpt-5.2
	// 只匹配纯数字版本号格式 gpt-X 或 gpt-X.Y，不匹配 gpt-4o 这种带字母后缀的
	if matches := openAIModelBasePattern.FindStringSubmatch(model); len(matches) > 1 {
		addVariant(matches[1])
	}

	// 3. 同时去掉日期后再提取基础版本号
	if withoutDate != model {
		if matches := openAIModelBasePattern.FindStringSubmatch(withoutDate); len(matches) > 1 {
			addVariant(matches[1])
		}
	}

	return variants
}

// GetStatus 获取服务状态
func (s *PricingService) GetStatus() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return map[string]any{
		"model_count":  len(s.pricingData),
		"last_updated": s.lastUpdated,
		"local_hash":   s.localHash[:min(8, len(s.localHash))],
	}
}

// ForceUpdate 强制更新
func (s *PricingService) ForceUpdate() error {
	return s.downloadPricingData()
}

// getPricingFilePath 获取价格文件路径
func (s *PricingService) getPricingFilePath() string {
	return filepath.Join(s.cfg.Pricing.DataDir, "model_pricing.json")
}

// getHashFilePath 获取哈希文件路径
func (s *PricingService) getHashFilePath() string {
	return filepath.Join(s.cfg.Pricing.DataDir, "model_pricing.sha256")
}

// ListModelNamesByProvider returns all model names in the catalog whose
// LiteLLMProvider matches the given provider string (case-insensitive).
// The returned slice is sorted alphabetically.
func (s *PricingService) ListModelNamesByProvider(provider string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	provider = strings.ToLower(strings.TrimSpace(provider))
	names := make([]string, 0)
	for name, p := range s.pricingData {
		if strings.ToLower(p.LiteLLMProvider) == provider {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// isNumeric 检查字符串是否为纯数字
func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
