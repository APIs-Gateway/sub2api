package service

// OpenAI prices GPT-5.6 requests whose input context exceeds 272K tokens at
// the long-context rate for the full request: input-side prices (including
// cache reads and writes) are doubled, while output is multiplied by 1.5.
const (
	gpt56LongContextTokenThreshold   = 272000
	gpt56LongContextInputMultiplier  = 2.0
	gpt56LongContextOutputMultiplier = 1.5
)
