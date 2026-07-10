# GPT-5.6 Cache-write Pricing

## §1 Goal

Make token billing for `gpt-5.6-sol`, `gpt-5.6-terra`, and `gpt-5.6-luna`
charge cache-write tokens at 1.25 times the effective input-token price.

This applies to the normal, flex, priority, and long-context price paths.

## §2 Scope and terminology

- A cache write is represented by the existing `CacheCreationTokens` field.
- A cache read is represented by `CacheReadTokens` and keeps its existing,
  independent rate.
- This change must not infer cache writes from OpenAI `cached_tokens`.
  That field reports cache reads only, and guessing a write count from it would
  overcharge ordinary input tokens.
- OpenAI-compatible usage may report an explicit root
  `cache_creation_input_tokens`, or a nested
  `input_tokens_details.cache_write_tokens` / `prompt_tokens_details.cache_write_tokens`.
  Normalize each explicit form to `CacheCreationTokens`.
- OpenAI `input_tokens` aggregates normal input, cache reads, and cache writes.
  Before invoking the shared calculator, bill normal input as:

  ```text
  normal_input_tokens = max(0, input_tokens - cache_read_tokens - cache_write_tokens)
  ```

  This makes the shared Claude-style cache-creation calculation correct without
  double-charging write tokens.  When no explicit write count is returned,
  cache-write cost remains zero rather than being inferred.

## §3 Price source and fallback rule

The live model-price catalog is authoritative when it provides an explicit
`cache_creation_input_token_cost` (and an optional priority equivalent).

When a known GPT-5.6 variant resolves to a pricing entry without an explicit
cache-write price, use the following recovery rule:

```text
cache_write_price = effective_input_price * 1.25
```

Apply the same rule independently to the priority input price.  An explicit
channel `cache_write_price` overrides both derived values.

The packaged catalog and hard-coded offline fallbacks must contain the current
per-model GPT-5.6 prices, so an unavailable or stale downloaded catalog does
not silently produce a zero or GPT-5.4 price.

## §4 Billing behavior

`computeTokenBreakdown` must select the cache-write rate for the active service
tier, then calculate cache creation with that rate.  Existing 5m/1h Anthropic
cache-breakdown behavior must remain unchanged.

For GPT-5.6, long-context pricing continues to apply the input-side multiplier
to cache writes. Flex continues to use the existing 0.5 tier multiplier.
Priority and long-context are not compounded: the upstream catalog publishes
them as separate rates, and Priority processing is not available for long
context requests.

## §5 Verification matrix

1. A stale dynamic GPT-5.6 catalog without cache-write fields derives 1.25x
   for Sol, Terra, Luna, aliases, and dated variants.
2. Explicit remote cache-write prices take precedence over derivation.
3. Normal, flex, priority, and long-context cache-write costs match the
   corresponding input price multiplied by 1.25.
4. An explicit channel cache-write override wins over the model default.
5. OpenAI usage with explicit root or nested cache-write tokens reaches the
   shared cache-creation calculation exactly once; a response containing only
   `cached_tokens` does not fabricate cache writes.

## §6 Implementation audit

- §1: The packaged catalog defines all published tiers, and both offline
  fallback layers define distinct Sol, Terra, and Luna base/priority prices
  from which the existing flex and long-context logic preserves the same 1.25x
  relationship.
- §2: Responses, raw Chat Completions, WebSocket, image-stream, and protocol
  compatibility paths parse an explicit cache-write field and preserve it as
  `CacheCreationTokens`; `RecordUsage` bills normal input as `raw - read -
  write`.
- §3: Stale dynamic catalog entries derive a missing write rate only for known
  GPT-5.6 models. Explicit catalog and channel values take precedence,
  including an explicit channel zero.
- §4: Standard, flex, priority, and long-context paths use the corresponding
  cache-write rate. Context thresholds reconstruct raw input from normal,
  cache-read, and cache-write tokens.
- §5: Unit coverage includes the three SKUs, stale and explicit catalog data,
  channel overrides, service tiers, long context, raw Chat, HTTP/SSE, WSv1,
  WSv2, cyber errors, and Responses compatibility. A live OpenAI-compatible
  Responses probe also confirmed the nested
  `input_tokens_details.cache_write_tokens` shape without inferring writes
  from cache reads.
