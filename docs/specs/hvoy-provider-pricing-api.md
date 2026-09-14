# Hvoy Provider Pricing API

## Goal

Expose `GET /api/provider/pricing` for hvoy.ai provider price crawling.

## Contract

- Return the hvoy provider pricing schema version `1.1` as bare JSON, not the internal `code/data` envelope.
- Currency is fixed to `CNY`.
- Default `price_unit` is `per_1m_tokens`.
- Publish the public price combinations:
  - `gpt-5.5` with group `codex plus`.
  - `gpt-5.6-sol` with group `codex plus`.
  - `gpt-5.6-terra` with group `codex plus`.
  - `gpt-5.6-luna` with group `codex plus`.
  - `gpt-6-astra` with group `codex plus`.
- `group_name` is the stable public group identifier for each combination.

## Pricing Rule

Use the service's balance recharge exchange setting as the fiat-to-USD conversion source.

The setting `balance_recharge_multiplier` means:

```text
credited_usd = fiat_amount * balance_recharge_multiplier
```

Therefore provider prices in CNY are:

```text
cny_per_1m_tokens = official_usd_per_token * group_rate_multiplier * 1_000_000 / balance_recharge_multiplier
```

`group_rate_multiplier` is the published group's rate multiplier (the "Nx" usage
rate billing applies on top of the official price). It is read from the active
group whose name matches the published `group_name` after collapsing whitespace
and case. If no such group exists (or its multiplier is not positive) the model
is published at 1x with a `note` explaining the gap.

Invalid or missing multipliers normalize to the existing default `1.0`.

## Data Source

Model prices come from `PricingService.GetModelPricing`.

That preserves the existing service fallback behavior where `gpt-5.5` resolves to the built-in GPT-5.4 static pricing, and `gpt-5.6-sol` / `gpt-5.6-terra` / `gpt-5.6-luna` / `gpt-6-astra` resolve to their built-in GPT-5.6 / GPT-6 Astra static pricing when the remote LiteLLM catalog is missing or stale.
