//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Route the configured source through real HTTP without changing its identity.
// The service still receives the default production URL for policy selection.
type pricingBoundaryTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t pricingBoundaryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	request := req.Clone(req.Context())
	request.URL.Scheme = t.target.Scheme
	request.URL.Host = t.target.Host
	return t.base.RoundTrip(request)
}

type pricingBoundaryHTTPClient struct{ client *http.Client }

func (c pricingBoundaryHTTPClient) FetchPricingJSON(ctx context.Context, source string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, &PricingRemoteHTTPStatusError{StatusCode: res.StatusCode}
	}
	return io.ReadAll(res.Body)
}

func (c pricingBoundaryHTTPClient) FetchHashText(ctx context.Context, source string) (string, error) {
	body, err := c.FetchPricingJSON(ctx, source)
	return strings.TrimSpace(string(body)), err
}

type pricingBoundaryCatalog struct {
	mu         sync.Mutex
	body       []byte
	status     int
	hashStatus int
	requests   int
}

func (c *pricingBoundaryCatalog) set(body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.body = body
}

func (c *pricingBoundaryCatalog) setStatus(status int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = status
}

func (c *pricingBoundaryCatalog) setHashStatus(status int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hashStatus = status
}

func (c *pricingBoundaryCatalog) downloads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests
}

func newPricingBoundaryHTTP(t *testing.T, body []byte) (*pricingBoundaryCatalog, PricingRemoteClient) {
	t.Helper()
	catalog := &pricingBoundaryCatalog{body: body}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		catalog.mu.Lock()
		defer catalog.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			if catalog.hashStatus != 0 {
				w.WriteHeader(catalog.hashStatus)
			}
			_, _ = io.WriteString(w, pssSHA(catalog.body)+"\n")
			return
		}
		catalog.requests++
		if catalog.status != 0 {
			w.WriteHeader(catalog.status)
		}
		_, _ = w.Write(catalog.body)
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	client := server.Client()
	client.Transport = pricingBoundaryTransport{target: target, base: client.Transport}
	return catalog, pricingBoundaryHTTPClient{client: client}
}

func pricingBoundaryPayload(input string) []byte {
	return []byte(fmt.Sprintf(`{
		"sample_spec":{"description":"keep documentation"},
		"other":{"input_cost_per_token":0.000007,"output_cost_per_token":0.000019,"custom_metadata":{"keep":true}},
		"codex-auto-review":{
			"input_cost_per_token":%s,"output_cost_per_token":0.000031,"cache_read_input_token_cost":0.000003,
			"input_cost_per_token_priority":0.000018,"output_cost_per_token_priority":0.000062,
			"input_cost_per_token_above_272k_tokens":0.000018,"output_cost_per_token_above_272k_tokens":0.0000465,
			"cache_creation_input_token_cost":0.000011,"cache_creation_input_token_cost_priority":0.000022,
			"cache_creation_input_token_cost_above_272k_tokens":0.000022,"cache_creation_input_token_cost_above_1hr":0.000024,
			"cache_read_input_token_cost_priority":0.000006,"cache_read_input_token_cost_above_272k_tokens":0.000006,
			"input_cost_per_token_batches":0.0000045,"output_cost_per_token_flex":0.0000155,
			"long_context_input_token_threshold":1234,"long_context_input_cost_multiplier":3,"long_context_output_cost_multiplier":2,
			"supports_service_tier":true,"litellm_provider":"openai","mode":"chat","supports_prompt_caching":true,
			"custom_metadata":{"keep":"internal"}
		}
	}`, input))
}

func newPricingBoundaryService(t *testing.T, dir string, remote PricingRemoteClient) *PricingService {
	t.Helper()
	return NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: dir, RemoteURL: config.DefaultPricingRemoteURL, HashURL: strings.TrimSuffix(config.DefaultPricingRemoteURL, ".json") + ".sha256", UpdateIntervalHours: 24, HashCheckIntervalMinutes: 60}}, remote)
}

func requirePricingBoundaryRates(t *testing.T, svc *PricingService) {
	t.Helper()
	require.Equal(t, &LiteLLMModelPricing{InputCostPerToken: 2e-7, OutputCostPerToken: 1.2e-6, CacheReadInputTokenCost: 2e-8, LiteLLMProvider: "openai", Mode: "chat", SupportsPromptCaching: true}, svc.GetModelPricing("codex-auto-review"))
}

func requirePricingBoundaryFileMatches(t *testing.T, svc *PricingService) []byte {
	t.Helper()
	body, err := os.ReadFile(svc.getPricingFilePath())
	require.NoError(t, err)
	parsed, err := (&PricingService{}).parsePricingData(body)
	require.NoError(t, err)
	svc.mu.RLock()
	require.Equal(t, svc.pricingData, parsed)
	svc.mu.RUnlock()
	return body
}

func TestPricingAutoSource_RemoteRefreshPersistRestartAndPin(t *testing.T) {
	ctx := context.Background()
	raw := pricingBoundaryPayload("0.000009")
	catalog, client := newPricingBoundaryHTTP(t, raw)
	svc := newPricingBoundaryService(t, t.TempDir(), client)
	require.NoError(t, svc.Initialize())
	t.Cleanup(svc.Stop)
	requirePricingBoundaryRates(t, svc)
	require.Equal(t, 0.000007, svc.GetModelPricing("other").InputCostPerToken)
	saved := requirePricingBoundaryFileMatches(t, svc)
	require.NotEqual(t, pssSHA(raw), pssSHA(saved), "effective content has its own SHA")
	require.Equal(t, pssSHA(raw), svc.localHash, "remote hash remains the synchronization anchor")
	var original, normalized map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &original))
	require.NoError(t, json.Unmarshal(saved, &normalized))
	require.JSONEq(t, string(original["other"]), string(normalized["other"]))
	require.JSONEq(t, string(original["sample_spec"]), string(normalized["sample_spec"]))
	var internal map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(normalized["codex-auto-review"], &internal))
	require.JSONEq(t, `{"keep":"internal"}`, string(internal["custom_metadata"]))
	require.Equal(t, json.RawMessage("false"), internal["supports_service_tier"])
	require.NotContains(t, internal, "input_cost_per_token_batches")
	require.NotContains(t, internal, "output_cost_per_token_flex")

	refreshed := []byte(strings.ReplaceAll(string(raw), "0.000007", "0.000008"))
	catalog.set(refreshed)
	require.NoError(t, svc.syncWithRemote())
	require.Equal(t, 2, catalog.downloads())
	requirePricingBoundaryRates(t, svc)
	require.Equal(t, 0.000008, svc.GetModelPricing("other").InputCostPerToken)
	saved = requirePricingBoundaryFileMatches(t, svc)
	require.NoError(t, svc.syncWithRemote())
	require.Equal(t, 2, catalog.downloads(), "unchanged remote hash must not download")

	restarted := newPricingBoundaryService(t, svc.cfg.Pricing.DataDir, client)
	store := newPSSStore()
	settings := newPSSSettings("auto")
	restarted.ConfigureSnapshots(settings, store, nil)
	require.NoError(t, restarted.Initialize())
	t.Cleanup(restarted.Stop)
	require.Equal(t, 3, catalog.downloads(), "restart re-establishes an anchor from a real download rather than trusting a legacy hash file")
	require.NoError(t, restarted.syncWithRemote())
	require.Equal(t, 3, catalog.downloads(), "the verified in-memory remote anchor avoids repeated periodic downloads")
	requirePricingBoundaryRates(t, restarted)
	pin, err := restarted.PinCurrentPricing(ctx, 42)
	require.NoError(t, err)
	require.Equal(t, pssSHA(saved), pin.Snapshot.ContentSHA256)
	require.Equal(t, saved, store.payloads[pin.Snapshot.ID])
	require.NoError(t, restarted.refreshSnapshotState(ctx))
	requirePricingBoundaryRates(t, restarted)
	offline := newPricingBoundaryService(t, t.TempDir(), nil)
	info, err := offline.LoadServicePricing(ctx, settings, store, func() (string, error) {
		t.Fatal("pinned replay must not resolve a file")
		return "", nil
	})
	require.NoError(t, err)
	require.Equal(t, pssSHA(saved), info.SHA256)
	require.Equal(t, 2, info.Models)
	requirePricingBoundaryRates(t, offline)
}

func TestPricingAutoSource_CustomAndMissingSKUPreservePayload(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   []byte
		custom bool
	}{
		{"custom", pricingBoundaryPayload("0.000009"), true},
		{"missing", []byte(`{"other":{"input_cost_per_token":0.000007}}`), false},
		{"invalid internal entry", []byte(`{"other":{"input_cost_per_token":0.000007},"codex-auto-review":{"mode":"chat"}}`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, client := newPricingBoundaryHTTP(t, tc.body)
			svc := newPricingBoundaryService(t, t.TempDir(), client)
			if tc.custom {
				svc.cfg.Pricing.RemoteURL = "https://custom.example/catalog.json"
			}
			require.NoError(t, svc.ForceUpdate())
			want, err := (&PricingService{}).parsePricingData(tc.body)
			require.NoError(t, err)
			require.Equal(t, want, svc.pricingData)
			require.Equal(t, tc.body, requirePricingBoundaryFileMatches(t, svc))
			if !tc.custom {
				require.Nil(t, svc.GetModelPricing("codex-auto-review"))
			}
			require.NoError(t, svc.checkAndUpdatePricing())
			require.Equal(t, want, svc.pricingData)
		})
	}
}

func TestPricingAutoSource_CachedAliasPreservesAgeAndCanPin(t *testing.T) {
	raw := pricingBoundaryPayload("0.000009")
	catalog, client := newPricingBoundaryHTTP(t, raw)
	catalog.setHashStatus(http.StatusBadRequest)
	svc := newPricingBoundaryService(t, t.TempDir(), client)
	pssWriteFile(t, svc.cfg.Pricing.DataDir, raw)
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(svc.getPricingFilePath(), old, old))
	require.NoError(t, os.WriteFile(svc.getHashFilePath(), []byte(pssSHA(raw)), 0o644))
	svc.ConfigureSnapshots(newPSSSettings("auto"), newPSSStore(), nil)
	require.NoError(t, svc.Initialize())
	t.Cleanup(svc.Stop)
	require.Zero(t, catalog.downloads())
	requirePricingBoundaryRates(t, svc)
	info, err := os.Stat(svc.getPricingFilePath())
	require.NoError(t, err)
	require.Equal(t, old, info.ModTime())
	requirePricingBoundaryFileMatches(t, svc)
	_, err = svc.PinCurrentPricing(context.Background(), 42)
	require.NoError(t, err)
}

func TestPricingAutoSource_DefaultAndCustomFallback(t *testing.T) {
	// t.Chdir also prevents use under parallel ancestors. The packaged source
	// is selected by the real default configuration, not a policy test switch.
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Dir(config.DefaultPricingFallbackFile), 0o755))
	raw := pricingBoundaryPayload("0.000009")
	require.NoError(t, os.WriteFile(config.DefaultPricingFallbackFile, raw, 0o644))
	customPath := filepath.Join(t.TempDir(), "custom.json")
	require.NoError(t, os.WriteFile(customPath, raw, 0o644))
	for _, fallback := range []string{config.DefaultPricingFallbackFile, customPath} {
		svc := newPricingBoundaryService(t, t.TempDir(), nil)
		svc.cfg.Pricing.FallbackFile = fallback
		svc.cfg.Pricing.HashURL = ""
		svc.ConfigureSnapshots(newPSSSettings("auto"), newPSSStore(), nil)
		require.NoError(t, svc.useFallbackPricing())
		if fallback == config.DefaultPricingFallbackFile {
			requirePricingBoundaryRates(t, svc)
		} else {
			require.Equal(t, 0.000009, svc.GetModelPricing("codex-auto-review").InputCostPerToken)
			require.True(t, svc.GetModelPricing("codex-auto-review").SupportsServiceTier)
			require.NoError(t, svc.checkAndUpdatePricing())
			require.Equal(t, 0.000009, svc.GetModelPricing("codex-auto-review").InputCostPerToken, "custom fallback cache retains its policy on startup")
		}
		requirePricingBoundaryFileMatches(t, svc)
		_, err := svc.PinCurrentPricing(context.Background(), 42)
		require.NoError(t, err)
	}
	packaged, err := os.ReadFile(config.DefaultPricingFallbackFile)
	require.NoError(t, err)
	require.Equal(t, raw, packaged, "fallback source is never rewritten")
}

func TestPricingAutoSource_PinnedStartupReloadAndOfflinePreserveApproved(t *testing.T) {
	for _, payload := range [][]byte{pricingBoundaryPayload("0.000009"), []byte(`{"other":{"input_cost_per_token":0.000007}}`)} {
		catalog, client := newPricingBoundaryHTTP(t, pricingBoundaryPayload("0.000099"))
		store := newPSSStore()
		store.setActive(1, payload)
		settings := newPSSSettings("pinned")
		svc := newPricingBoundaryService(t, t.TempDir(), client)
		svc.ConfigureSnapshots(settings, store, nil)
		require.NoError(t, svc.Initialize())
		t.Cleanup(svc.Stop)
		want, err := (&PricingService{}).parsePricingData(payload)
		require.NoError(t, err)
		require.Equal(t, want, svc.pricingData)
		store.setActive(2, payload)
		require.NoError(t, svc.refreshSnapshotState(context.Background()))
		require.Equal(t, want, svc.pricingData)
		require.Zero(t, catalog.downloads())
		require.Empty(t, store.inserted)
		require.Empty(t, store.activated)
		_, err = os.Stat(svc.getPricingFilePath())
		require.True(t, os.IsNotExist(err))
		file := filepath.Join(t.TempDir(), "chosen.json")
		require.NoError(t, os.WriteFile(file, payload, 0o644))
		for _, snapshot := range []bool{false, true} {
			offline := newPricingBoundaryService(t, t.TempDir(), nil)
			var info PricingDataInfo
			if snapshot {
				info, err = offline.LoadServicePricing(context.Background(), settings, store, func() (string, error) {
					t.Fatal("approved replay must not resolve a file")
					return "", nil
				})
			} else {
				info, err = offline.LoadOfflinePricing(file)
			}
			require.NoError(t, err)
			require.Equal(t, pssSHA(payload), info.SHA256)
			require.Equal(t, len(want), info.Models)
			require.Equal(t, want, offline.pricingData)
			after, err := os.ReadFile(file)
			require.NoError(t, err)
			require.Equal(t, payload, after)
		}
	}
}

func TestPricingAutoSource_CandidateDiffHoldAndApprovalAreFaithful(t *testing.T) {
	for _, hold := range []bool{false, true} {
		t.Run(fmt.Sprintf("hold=%t", hold), func(t *testing.T) {
			ctx := context.Background()
			active := pricingBoundaryPayload("0.000009")
			candidate := pricingBoundaryPayload("0.000017")
			_, client := newPricingBoundaryHTTP(t, candidate)
			store := newPSSStore()
			store.setActive(1, active)
			svc := newPricingBoundaryService(t, t.TempDir(), client)
			svc.ConfigureSnapshots(newPSSSettings("pinned"), store, nil)
			require.NoError(t, svc.Initialize())
			t.Cleanup(svc.Stop)
			fetched, err := svc.FetchCandidateSnapshot(ctx, nil)
			require.NoError(t, err)
			require.True(t, fetched.Created)
			require.Equal(t, pssSHA(candidate), fetched.Candidate.ContentSHA256)
			require.Equal(t, candidate, store.payloads[fetched.Candidate.ID])
			require.Equal(t, 0.000009, svc.GetModelPricing("codex-auto-review").InputCostPerToken, "fetching never changes billing")
			admin := NewPricingSnapshotAdminService(svc, store, &pssChecker{}, nil)
			plan, err := admin.Preview(ctx, fetched.Candidate.ID, nil)
			require.NoError(t, err)
			require.Len(t, plan.Entries, 1)
			require.Equal(t, "codex-auto-review", plan.Entries[0].ModelKey)
			require.Equal(t, []string{"input_cost_per_token"}, plan.Entries[0].ChangedFields)
			require.Equal(t, 0.000009, plan.Entries[0].Old.InputCostPerToken)
			require.Equal(t, 0.000017, plan.Entries[0].New.InputCostPerToken)
			var held []string
			if hold {
				held = []string{"codex-auto-review"}
				plan, err = admin.Preview(ctx, fetched.Candidate.ID, held)
				require.NoError(t, err)
				require.Equal(t, PricingDiffDecisionHold, plan.Entries[0].Decision)
				require.Equal(t, 0.000009, plan.mergedData["codex-auto-review"].InputCostPerToken)
				require.Equal(t, pssSHA(plan.MergedPayload), plan.MergedSHA256)
				_, err = admin.Approve(ctx, fetched.Candidate.ID, held, plan.PlanHash, 42)
				require.ErrorIs(t, err, ErrPricingSnapshotNothingToApprove)
				require.Equal(t, 0.000009, svc.GetModelPricing("codex-auto-review").InputCostPerToken)
				require.Empty(t, store.applied)
				held = nil
				plan, err = admin.Preview(ctx, fetched.Candidate.ID, nil)
				require.NoError(t, err)
			}
			approved, err := admin.Approve(ctx, fetched.Candidate.ID, held, plan.PlanHash, 42)
			require.NoError(t, err)
			require.Equal(t, 0.000017, svc.GetModelPricing("codex-auto-review").InputCostPerToken)
			require.True(t, svc.GetModelPricing("codex-auto-review").SupportsServiceTier)
			require.Equal(t, pssSHA(plan.MergedPayload), approved.ContentSHA256)
			require.Equal(t, plan.MergedPayload, store.payloads[approved.ID])
			require.Equal(t, active, store.payloads[1], "historical approved bytes remain untouched")
			require.Equal(t, pssSHA(active), store.metas[1].ContentSHA256)
		})
	}
}

func TestPricingAutoSource_CustomFallbackAndDefaultDownloadSurviveRestart(t *testing.T) {
	for _, withHash := range []bool{false, true} {
		t.Run(fmt.Sprintf("remote_hash=%t", withHash), func(t *testing.T) {
			ctx := context.Background()
			raw := pricingBoundaryPayload("0.000009")
			catalog, client := newPricingBoundaryHTTP(t, raw)
			catalog.setStatus(http.StatusBadRequest)
			customFile := filepath.Join(t.TempDir(), "custom-fallback.json")
			require.NoError(t, os.WriteFile(customFile, raw, 0o644))
			svc := newPricingBoundaryService(t, t.TempDir(), client)
			svc.cfg.Pricing.FallbackFile = customFile
			if !withHash {
				svc.cfg.Pricing.HashURL = ""
			}
			require.NoError(t, svc.Initialize(), "failed default remote uses the configured custom fallback")
			t.Cleanup(svc.Stop)
			require.Equal(t, 0.000009, svc.GetModelPricing("codex-auto-review").InputCostPerToken)
			require.Equal(t, raw, requirePricingBoundaryFileMatches(t, svc))
			require.Equal(t, 1, catalog.downloads())
			if withHash {
				// A matching old synchronization anchor does not prove that these
				// ambiguous bytes came from the default remote catalog.
				require.NoError(t, os.WriteFile(svc.getHashFilePath(), []byte(pssSHA(raw)), 0o644))
				old := time.Now().Add(-48 * time.Hour)
				require.NoError(t, os.Chtimes(svc.getPricingFilePath(), old, old))
			}

			restarted := NewPricingService(svc.cfg, client)
			require.NoError(t, restarted.Initialize())
			t.Cleanup(restarted.Stop)
			require.Equal(t, 1, catalog.downloads(), "ambiguous cache is preserved even when its old remote hash still matches")
			require.Equal(t, 0.000009, restarted.GetModelPricing("codex-auto-review").InputCostPerToken)
			require.Equal(t, raw, requirePricingBoundaryFileMatches(t, restarted))
			require.Equal(t, pssSHA(raw), restarted.localHash)

			// A real download from the configured default source has explicit authority,
			// even when that configuration also has a custom fallback.
			catalog.setStatus(http.StatusOK)
			require.NoError(t, restarted.ForceUpdate())
			require.Equal(t, 2, catalog.downloads())
			requirePricingBoundaryRates(t, restarted)
			effective := requirePricingBoundaryFileMatches(t, restarted)
			require.NotEqual(t, pssSHA(raw), pssSHA(effective))
			wantAnchor := pssSHA(effective)
			if withHash {
				wantAnchor = pssSHA(raw)
			}
			require.Equal(t, wantAnchor, restarted.localHash)

			store := newPSSStore()
			afterDownload := NewPricingService(svc.cfg, client)
			afterDownload.ConfigureSnapshots(newPSSSettings("auto"), store, nil)
			require.NoError(t, afterDownload.Initialize())
			t.Cleanup(afterDownload.Stop)
			wantDownloads := 2
			if withHash {
				wantDownloads++
			}
			require.Equal(t, wantDownloads, catalog.downloads(), "normalized cache is revalidated against the remote instead of trusting a bare saved hash")
			requirePricingBoundaryRates(t, afterDownload)
			require.Equal(t, effective, requirePricingBoundaryFileMatches(t, afterDownload))
			require.Equal(t, wantAnchor, afterDownload.localHash)
			pinned, err := afterDownload.PinCurrentPricing(ctx, 42)
			require.NoError(t, err)
			require.Equal(t, pssSHA(effective), pinned.Snapshot.ContentSHA256)
			require.Equal(t, effective, store.payloads[pinned.Snapshot.ID])
			require.NoError(t, afterDownload.refreshSnapshotState(ctx))
			requirePricingBoundaryRates(t, afterDownload)
		})
	}
}

func TestPricingAutoSource_ExpiredCacheWithoutHashStillRefreshes(t *testing.T) {
	raw := pricingBoundaryPayload("0.000009")
	remote := []byte(strings.ReplaceAll(string(raw), "0.000007", "0.000008"))
	catalog, client := newPricingBoundaryHTTP(t, remote)
	svc := newPricingBoundaryService(t, t.TempDir(), client)
	svc.cfg.Pricing.HashURL = ""
	pssWriteFile(t, svc.cfg.Pricing.DataDir, raw)
	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(svc.getPricingFilePath(), old, old))
	require.NoError(t, svc.Initialize())
	t.Cleanup(svc.Stop)
	require.Equal(t, 1, catalog.downloads(), "normalizing an expired cache must not make it fresh")
	require.Equal(t, 0.000008, svc.GetModelPricing("other").InputCostPerToken)
	requirePricingBoundaryRates(t, svc)
	requirePricingBoundaryFileMatches(t, svc)
}

func TestPricingAutoSource_FailedPersistenceDoesNotPublishEffectivePricing(t *testing.T) {
	raw := pricingBoundaryPayload("0.000009")
	_, client := newPricingBoundaryHTTP(t, raw)
	svc := newPricingBoundaryService(t, t.TempDir(), client)
	before, err := svc.parsePricingData(raw)
	require.NoError(t, err)
	svc.mu.Lock()
	svc.setPricingDataLocked(before)
	svc.mu.Unlock()
	// A directory in place of the cache file fails deterministically even
	// when CI runs with permission to write otherwise read-only files.
	require.NoError(t, os.Mkdir(svc.getPricingFilePath(), 0o755))
	require.ErrorContains(t, svc.ForceUpdate(), "save pricing file")
	require.Equal(t, before, svc.pricingData)
	require.Equal(t, 0.000009, svc.GetModelPricing("codex-auto-review").InputCostPerToken)
	require.Empty(t, svc.localHash)
	_, err = os.Stat(svc.getHashFilePath())
	require.True(t, os.IsNotExist(err), "a failed payload write must not persist a successful sync anchor")
}

func TestPricingAutoSource_ApproveOtherChangeKeepsHeldInternalSKU(t *testing.T) {
	ctx := context.Background()
	active := pricingBoundaryPayload("0.000009")
	candidate := []byte(strings.ReplaceAll(string(pricingBoundaryPayload("0.000017")), "0.000007", "0.000008"))
	_, client := newPricingBoundaryHTTP(t, candidate)
	store := newPSSStore()
	store.setActive(1, active)
	settings := newPSSSettings("pinned")
	svc := newPricingBoundaryService(t, t.TempDir(), client)
	svc.ConfigureSnapshots(settings, store, nil)
	require.NoError(t, svc.Initialize())
	t.Cleanup(svc.Stop)
	fetched, err := svc.FetchCandidateSnapshot(ctx, nil)
	require.NoError(t, err)
	admin := NewPricingSnapshotAdminService(svc, store, &pssChecker{}, nil)
	held := []string{"codex-auto-review"}
	plan, err := admin.Preview(ctx, fetched.Candidate.ID, held)
	require.NoError(t, err)
	require.Equal(t, 1, plan.Stats.Held)
	require.Equal(t, 1, plan.Stats.Approved)
	approved, err := admin.Approve(ctx, fetched.Candidate.ID, held, plan.PlanHash, 42)
	require.NoError(t, err)
	require.Equal(t, 0.000009, svc.GetModelPricing("codex-auto-review").InputCostPerToken)
	require.True(t, svc.GetModelPricing("codex-auto-review").SupportsServiceTier)
	require.Equal(t, 0.000008, svc.GetModelPricing("other").InputCostPerToken)
	require.Equal(t, pssSHA(plan.MergedPayload), approved.ContentSHA256)
	require.Equal(t, plan.MergedPayload, store.payloads[approved.ID])
	require.Equal(t, active, store.payloads[1])
	var old, merged map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(active, &old))
	require.NoError(t, json.Unmarshal(plan.MergedPayload, &merged))
	require.JSONEq(t, string(old["codex-auto-review"]), string(merged["codex-auto-review"]))
	offline := newPricingBoundaryService(t, t.TempDir(), nil)
	info, err := offline.LoadServicePricing(ctx, settings, store, func() (string, error) {
		t.Fatal("held snapshot replay must not resolve a file")
		return "", nil
	})
	require.NoError(t, err)
	require.Equal(t, pssSHA(plan.MergedPayload), info.SHA256)
	require.Equal(t, 2, info.Models)
	require.Equal(t, 0.000009, offline.GetModelPricing("codex-auto-review").InputCostPerToken)
	require.Equal(t, 0.000008, offline.GetModelPricing("other").InputCostPerToken)
}

func TestPricingAutoSource_FallbackInvalidatesPreviousRemoteAnchor(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Dir(config.DefaultPricingFallbackFile), 0o755))
	fallback := pricingBoundaryPayload("0.000009")
	require.NoError(t, os.WriteFile(config.DefaultPricingFallbackFile, fallback, 0o644))
	customFile := filepath.Join(t.TempDir(), "custom-fallback.json")
	require.NoError(t, os.WriteFile(customFile, fallback, 0o644))
	remote := []byte(strings.ReplaceAll(string(pricingBoundaryPayload("0.000017")), "0.000007", "0.000008"))
	for _, source := range []string{config.DefaultPricingFallbackFile, customFile} {
		for _, restart := range []bool{false, true} {
			t.Run(fmt.Sprintf("packaged=%t/restart=%t", source == config.DefaultPricingFallbackFile, restart), func(t *testing.T) {
				catalog, client := newPricingBoundaryHTTP(t, remote)
				original := newPricingBoundaryService(t, t.TempDir(), client)
				original.cfg.Pricing.FallbackFile = source
				require.NoError(t, original.Initialize())
				t.Cleanup(original.Stop)
				require.Equal(t, pssSHA(remote), original.localHash)
				require.Equal(t, 0.000008, original.GetModelPricing("other").InputCostPerToken)
				anchor, err := os.ReadFile(original.getHashFilePath())
				require.NoError(t, err)
				require.Equal(t, pssSHA(remote), strings.TrimSpace(string(anchor)))

				// Keep H on disk while the cache becomes invalid and the remote
				// catalog fails. The hash endpoint still advertises the same H.
				require.NoError(t, os.WriteFile(original.getPricingFilePath(), []byte("invalid cache"), 0o644))
				catalog.setStatus(http.StatusBadRequest)
				usingFallback := NewPricingService(original.cfg, client)
				require.NoError(t, usingFallback.Initialize())
				t.Cleanup(usingFallback.Stop)
				require.Equal(t, 2, catalog.downloads())
				require.Equal(t, 0.000007, usingFallback.GetModelPricing("other").InputCostPerToken)
				require.NotEqual(t, pssSHA(remote), usingFallback.localHash)
				requirePricingBoundaryFileMatches(t, usingFallback)
				_, err = os.Stat(usingFallback.getHashFilePath())
				require.True(t, os.IsNotExist(err), "fallback F cannot inherit the old remote H")

				catalog.setStatus(http.StatusOK)
				recovered := usingFallback
				if restart {
					recovered = NewPricingService(original.cfg, client)
					require.NoError(t, recovered.Initialize())
					t.Cleanup(recovered.Stop)
				} else {
					require.NoError(t, recovered.syncWithRemote())
				}
				require.Equal(t, 3, catalog.downloads(), "same remote H must recover after fallback replacement")
				require.Equal(t, 0.000008, recovered.GetModelPricing("other").InputCostPerToken)
				requirePricingBoundaryRates(t, recovered)
				requirePricingBoundaryFileMatches(t, recovered)
				require.Equal(t, pssSHA(remote), recovered.localHash)
			})
		}
	}
}

func TestPricingAutoSource_CustomRemoteCacheFailurePreservesAvailability(t *testing.T) {
	raw := pricingBoundaryPayload("0.000009")
	_, client := newPricingBoundaryHTTP(t, raw)
	svc := newPricingBoundaryService(t, t.TempDir(), client)
	svc.cfg.Pricing.RemoteURL = "https://custom.example/catalog.json"
	require.NoError(t, os.Mkdir(svc.getPricingFilePath(), 0o755))
	require.NoError(t, svc.Initialize(), "legacy custom remote loads actual remote data despite an unwritable cache")
	t.Cleanup(svc.Stop)
	require.Equal(t, 0.000009, svc.GetModelPricing("codex-auto-review").InputCostPerToken)
	require.True(t, svc.GetModelPricing("codex-auto-review").SupportsServiceTier)
	require.Equal(t, pssSHA(raw), svc.localHash)
	info, err := os.Stat(svc.getPricingFilePath())
	require.NoError(t, err)
	require.True(t, info.IsDir(), "the failed cache must not be represented as persisted pricing")
	// This legacy availability mode cannot promise a successful file pin.
	svc.ConfigureSnapshots(newPSSSettings("auto"), newPSSStore(), nil)
	_, err = svc.PinCurrentPricing(context.Background(), 42)
	require.Error(t, err)
}

func TestPricingAutoSource_CustomFallbackCacheFailurePreservesAvailability(t *testing.T) {
	raw := pricingBoundaryPayload("0.000009")
	catalog, client := newPricingBoundaryHTTP(t, pricingBoundaryPayload("0.000017"))
	catalog.setStatus(http.StatusBadRequest)
	file := filepath.Join(t.TempDir(), "custom-fallback.json")
	require.NoError(t, os.WriteFile(file, raw, 0o644))
	for _, anchorBlocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("anchor_clear_blocked=%t", anchorBlocked), func(t *testing.T) {
			svc := newPricingBoundaryService(t, t.TempDir(), client)
			svc.cfg.Pricing.FallbackFile = file
			if anchorBlocked {
				// A nonempty directory makes anchor invalidation fail even with
				// elevated filesystem permissions; do not replace the old cache.
				require.NoError(t, os.WriteFile(svc.getPricingFilePath(), []byte("old invalid cache"), 0o644))
				require.NoError(t, os.Mkdir(svc.getHashFilePath(), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(svc.getHashFilePath(), "owned-test-marker"), []byte("keep"), 0o644))
			} else {
				require.NoError(t, os.Mkdir(svc.getPricingFilePath(), 0o755))
			}
			require.NoError(t, svc.Initialize(), "readable custom fallback remains available when caching fails")
			t.Cleanup(svc.Stop)
			require.Equal(t, 0.000009, svc.GetModelPricing("codex-auto-review").InputCostPerToken)
			require.True(t, svc.GetModelPricing("codex-auto-review").SupportsServiceTier)
			require.Equal(t, pssSHA(raw), svc.localHash)
			unchanged, err := os.ReadFile(file)
			require.NoError(t, err)
			require.Equal(t, raw, unchanged)
			if anchorBlocked {
				cached, err := os.ReadFile(svc.getPricingFilePath())
				require.NoError(t, err)
				require.Equal(t, []byte("old invalid cache"), cached, "failed invalidation must prevent writing F beside an old anchor")
			}
		})
	}
}

func TestPricingAutoSource_LegacyValidFallbackCacheDoesNotTrustStaleAnchor(t *testing.T) {
	remote := []byte(strings.ReplaceAll(string(pricingBoundaryPayload("0.000017")), "0.000007", "0.000008"))
	// Both forms can have been written by an older fallback. Already having
	// the three default rates does not bind these bytes to remote hash H.
	normalizedFallback := []byte(`{"other":{"input_cost_per_token":0.000007},"codex-auto-review":{"input_cost_per_token":2e-7,"output_cost_per_token":1.2e-6,"cache_read_input_token_cost":2e-8,"supports_service_tier":false,"litellm_provider":"openai","mode":"chat","supports_prompt_caching":true}}`)
	for _, customFallback := range []bool{false, true} {
		for _, cached := range [][]byte{pricingBoundaryPayload("0.000009"), normalizedFallback} {
			t.Run(fmt.Sprintf("custom=%t/cacheSHA=%s", customFallback, pssSHA(cached)[:8]), func(t *testing.T) {
				catalog, client := newPricingBoundaryHTTP(t, remote)
				svc := newPricingBoundaryService(t, t.TempDir(), client)
				if customFallback {
					svc.cfg.Pricing.FallbackFile = filepath.Join(t.TempDir(), "unused-custom-fallback.json")
				} else {
					svc.cfg.Pricing.FallbackFile = config.DefaultPricingFallbackFile
				}
				pssWriteFile(t, svc.cfg.Pricing.DataDir, cached)
				require.NoError(t, os.WriteFile(svc.getHashFilePath(), []byte(pssSHA(remote)), 0o644))
				svc.ConfigureSnapshots(newPSSSettings("auto"), newPSSStore(), nil)
				require.NoError(t, svc.Initialize(), "valid legacy cache must be compared by its own content, without another fallback load")
				t.Cleanup(svc.Stop)
				require.Equal(t, 1, catalog.downloads(), "legacy unbound H must not suppress recovery to R")
				require.Equal(t, 0.000008, svc.GetModelPricing("other").InputCostPerToken)
				requirePricingBoundaryRates(t, svc)
				effective := requirePricingBoundaryFileMatches(t, svc)
				require.Equal(t, pssSHA(remote), svc.localHash)
				require.NoError(t, svc.syncWithRemote())
				require.Equal(t, 1, catalog.downloads(), "after actual recovery the in-memory remote anchor is safe for periodic comparison")
				pin, err := svc.PinCurrentPricing(context.Background(), 42)
				require.NoError(t, err)
				require.Equal(t, pssSHA(effective), pin.Snapshot.ContentSHA256)
			})
		}
	}
}
