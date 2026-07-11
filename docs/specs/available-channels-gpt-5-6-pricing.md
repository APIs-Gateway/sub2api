# Available Channels: GPT-5.6 Official Pricing

## §1 Goal and availability boundary

Make the user-facing **Pricing & Billing** (`/available-channels`) cards render
complete official Standard pricing for these models when an active OpenAI
channel actually exposes them:

- `gpt-5.6-sol`
- `gpt-5.6-terra`
- `gpt-5.6-luna`

The page remains a channel-availability view, not a generic model catalogue.
It must not fabricate a card for a model that is absent from the channel's
exact `model_mapping` and `model_pricing` configuration.  Administrators can
select exact mapped models in the existing public-display settings UI.

There is no versioned default channel configuration in the repository. To make
these cards appear for an actual OpenAI channel, add one empty
`billing_mode=token` pricing entry containing all three model names. It makes
the gateway allow them when `restrict_models=true`, while the all-empty price
fields continue to fall back to the official display catalogue. The requests
then pass through with their original model names. Add an exact model mapping
only when the verified upstream requires a different name; do not infer it
from an existing wildcard mapping.

## §2 Official Standard prices

All values are USD per 1M tokens, sourced from OpenAI's flagship pricing table
on 2026-07-10.

| Model | Short input | Short cache read | Short cache write | Short output | Long input | Long cache read | Long cache write | Long output |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `gpt-5.6-sol` | 5 | 0.50 | 6.25 | 30 | 10 | 1 | 12.50 | 45 |
| `gpt-5.6-terra` | 2.50 | 0.25 | 3.125 | 15 | 5 | 0.50 | 6.25 | 22.50 |
| `gpt-5.6-luna` | 1 | 0.10 | 1.25 | 6 | 2 | 0.20 | 2.50 | 9 |

OpenAI defines long context as a request with **more than 272K input tokens**.
For the complete request, input, cache read, and cache write are 2× the short
context rate, while output is 1.5×.  Cache write is 1.25× the corresponding
uncached input rate.

Source: <https://developers.openai.com/api/docs/pricing#flagship-models> and
the GPT-5.6 Sol, Terra, and Luna model pages.

## §3 Data path

1. Preserve the `*_above_272k_tokens` fields from the packaged pricing
   catalogue when parsing it.
2. Synthesize two display intervals for the GPT-5.6 family: `(0, 272K]` and
   `(272K, ∞]`, including all four token price dimensions.
3. Use the named GPT-5.6 fallback when a dynamic catalogue does not contain
   those long-context fields, so a stale catalogue cannot hide official prices.
4. Keep channel-specific prices authoritative.  The global catalogue is only a
   display fallback for a model whose channel price is entirely empty.

## §4 UI behavior

The model card continues to show the default (short-context) price rows.  Its
long-context section must show input, output, cache read, and cache write for
each interval, with the selected group multiplier applied in the effective-cost
section.  It must not claim Batch, Flex, or Priority access: those service
tiers are not represented by the channel configuration used by this page.

## §5 Verification

- Unit-test catalogue parsing for the above-272K price fields.
- Unit-test synthesized GPT-5.6 Sol/Terra/Luna intervals and all four prices.
- Unit-test model-card rendering of long-context cache read/write prices.
- Unit-test the public-display settings candidate list includes exact mapping
  sources as well as explicit channel pricing models.
- Run targeted Go and Vitest suites, frontend typecheck/lint, and the relevant
  static checks before handoff.
