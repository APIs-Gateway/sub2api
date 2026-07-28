# Claude Upstream Sync Design

## Goal

Bring this fork's Claude, Anthropic, Bedrock, and Claude Code behavior in line
with the current `Wei-Shaw/sub2api` `main` branch without merging unrelated
upstream product work.

## Source of truth and boundary

The source of truth is the final state of `upstream/main`, not the sequence
of historical commits. The sync includes runtime behavior and its regression
tests in the following domains:

- Claude model registration, pricing, Bedrock mapping, Vertex beta filtering,
  thinking, cache, and tool-search capability gates.
- Anthropic API-key and OAuth forwarding, Claude Code detection/mimicry,
  prompt-cache preservation, and account header configuration.
- Anthropic Messages, OpenAI Responses, and Chat Completions conversion,
  streaming event semantics, tools, stop reasons, usage, and error paths.
- Anthropic rate-limit, usage-window, and synthetic-response wire behavior.

Changes outside these domains are intentionally excluded even if they appear in
the same upstream commits.

## Integration approach

The fork has substantial independent work, so no broad merge or blind
cherry-pick is allowed. Each upstream functional group is reconciled into the
fork's current architecture, retaining fork-specific behavior outside the
boundary. Upstream regression tests are ported alongside each group.

The work is organized in dependency order:

1. Model/capability primitives and account configuration.
2. Claude Code/OAuth request normalization and cache protection.
3. Messages/Responses/Chat protocol conversion and synthetic wire parity.
4. Rate-limit and usage behavior.

## Verification and release

Per the issue request, no local test, build, compile, or lint command is run.
The pull request is the only validation surface. It must reference and close
the fork issue; the issue is updated with the PR URL. Remote CI must pass and
the repository's coverage result must be at least 80%.

## Non-goals

- Merging the full upstream repository history.
- Syncing non-Claude provider features merely because they share a file.
- Changing protected repository branding or metadata.
