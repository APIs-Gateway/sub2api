# Hvoy Provider Pricing API

## Goal

Expose `GET /api/provider/pricing` for hvoy.ai provider price crawling.

## Contract

- Return the hvoy provider pricing schema version `1.1` as bare JSON, not the internal `code/data` envelope.
- Currency is fixed to `CNY`.
- Default `price_unit` is `per_1m_tokens`.
- Publish the public price combinations:
  - `gpt-5.5` with group `codex plus`.
  - `gpt-5.4` with group `codex plus`.
  - `gpt-5.6-sol` with group `codex plus`.
  - `gpt-5.6-terra` with group `codex plus`.
- `group_name` is the stable public group identifier for each combination.

## Pricing Rule

Use the service's balance recharge exchange setting as the fiat-to-USD conversion source.

The setting `balance_recharge_multiplier` means:

```text
credited_usd = fiat_amount * balance_recharge_multiplier
```

Therefore provider prices in CNY are:

```text
cny_per_1m_tokens = official_usd_per_token * 1_000_000 / balance_recharge_multiplier
```

Invalid or missing multipliers normalize to the existing default `1.0`.

## Data Source

Model prices come from `PricingService.GetModelPricing`.

That preserves the existing service fallback behavior where `gpt-5.5` and `gpt-5.4` resolve to the built-in GPT-5.4 static pricing, and `gpt-5.6-sol` / `gpt-5.6-terra` resolve to their built-in GPT-5.6 static pricing when the remote LiteLLM catalog is missing or stale.
