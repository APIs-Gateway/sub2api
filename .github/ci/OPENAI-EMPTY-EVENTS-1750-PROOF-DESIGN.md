# #1750 formatter and subsequent behavior proof

Behavior readiness: **false**. This branch contains a formatter only. There is
no behavior workflow or executable test command to accidentally run RAW source.
No application, compiler, syntax checker, formatter, test or project validator
was run locally or on server 87 to author this controller.

## Frozen RAW input and actual format gate

OLD `5052c03eb672b7cd162bf3fa41042aa35d34cc25`, tree
`84ccc9c8b4ccbce9b35a642730089cb0813300ea`, is test-only atop origin/main
`c3e3eed31c7783d1923e1afd9ce706f3501ece26`. NEW
`a2cebe17c05e5c806bd4019f8085c35864345f09`, tree
`bcf2097c5193b1ffc24d8b540b9e35daf09f8244`, changes production/provenance
only. Both contain exactly 4307 tracked inputs. The controller contains the
entire NEW tree byte for byte plus only these three controller files (4310).
The complete stage-zero index, tree, modes, actual file/blob/SHA256 bytes and
full Git archives include hidden files and shared frontend/fixture inputs.
Clean before/after snapshots reject tracked, untracked or ignored additions.

Both test inputs are exactly 14023 bytes, SHA256
`281d29b9d886947737f484f8605f20fda91661ef6c4c64080534c4be806f3a10`.
Go 1.26.10 executes raw-diff, write-copy, formatted-diff and formatted-list on
each OLD/NEW test and production copy, outside all checkouts: sixteen actual
operations, with argv, cwd, process timing, actual exit, stdout and stderr.
Actual Go/gofmt binary hashes and toolchain environment are retained. All
post-format operations require zero exit and empty stdio. Raw-diff must agree
with whether actual output bytes changed. Eight remote-only checker controls
must reject missing/signal/timeout exit, residual list, stderr, hidden/invented
diff, and nonzero post-diff. These are checker controls, not application tests.
OLD/NEW raw and formatted test copies must each be byte identical.

**OLD production is evidence only. Do not import its formatted output.** It
must remain the original main production blob. After actual artifact review by
root and an independent reviewer, import only the same formatted test into new
normal OLD/NEW heads, and NEW production if changed. Preserve original RAW
heads, update provenance honestly, then freeze the new pins and obtain source
review. If output is unchanged, retain the original head; do not make an empty
commit. Never edit a CI checkout and describe the result as its Git head.

## Subsequent independent OLD/NEW behavior workflow (not implemented here)

Only after actual format acceptance and final source review, author/freeze a
separate controller with exact formatted heads/trees and identical test SHA.
It must be independently reviewed before any publication. OLD and NEW are
independent jobs, never fail-fast/cancel each other. OLD's actual nonzero test
exit remains a failed job even if its complete evidence demonstrates RED.
Actual build/fixture/race/panic/watchdog failures cannot be business RED.

From backend, run the same three top tests in internal/service with actual Go
1.26.10, `-tags=unit -mod=readonly -p=1 -parallel=1 -race -count=1 -json`:

- `TestOpenAIEmptyEventPreOutputRetry`: 15 literal events × native/passthrough/
  bridge = 45 leaves. The 15 are text_delta, summary_delta, reasoning_delta,
  function_delta, custom_delta, text_done, summary_done, reasoning_done,
  refusal_delta, refusal_done, content_done, summary_part_done,
  content_refusal_done, queued, metadata.
- `TestOpenAIEmptyEventCommittedOutputDoesNotReplay`: 8 events × 3 paths = 24
  leaves: whitespace_is_output, function_arguments, custom_input,
  function_completion, custom_completion, encrypted_reasoning, unknown_event,
  event_label_cannot_hide_completion.
- `TestOpenAIEmptyEventConservativeClassification`: 29 leaves: delta_missing,
  delta_null, delta_object, delta_number, delta_false, text_missing, text_null,
  refusal_null, part_missing, part_null, part_array, part_text_missing,
  part_text_null, part_text_object, part_refusal_missing, part_unknown,
  part_type_missing, summary_text_missing, summary_text_null, summary_wrong_type,
  invalid_json, payload_array, type_missing, type_null, contradictory_type,
  metadata_type_mismatch, duplicate_type, duplicate_delta, duplicate_part_text.

Require the exact 98 leaf identities, three top tests and six path parents to
RUN and terminate once, no unknown tests, skip or missing records. NEW requires
all PASS and package PASS, all 69 real-wire prerequisites exactly once, and all
45 pre-output completion markers exactly once. Retain raw test JSON/stdio,
actual command argv/cwd/exit/timing and full checkout source/index/archive
before/after. Verify actual Go.mod/go.sum stay unchanged and retain go-list
module/package evidence. Existing relevant retry/billing/budget/keepalive/
first-output/bridge-disconnect regression tests and full formal CI remain
required independently; the focused 98 do not replace those gates.

OLD must first have the complete actual 98-leaf record and all 69 real peer
completions. For every failed leaf, correlate its actual business assertion
marker with that same test's JSON output and completed prerequisites. Do not
invent a failure whitelist or accept a package/compiler marker as a test.
`EMPTY_EVENT_FIXTURE`, race, panic, timeout, incomplete event stream, missing
process exit, unrelated assertion failures or uncaptured stderr block RED.
Require at least one actual pre-output business assertion failure; a fake text
marker alone is not evidence. Preserve all successful/failed actual outcomes.
Remote classifier negative controls must reject missing/duplicate leaf events,
missing actual peer completion, fixture/compiler/race/watchdog outputs,
fabricated markers, and incomplete or contradictory process exits. Controls
are separately identified and never counted as application results.

## Approval scope

This formatter performs no application test/build and grants no runtime or
coverage approval. origin/main's older security baseline and PR1712's security,
Windows, aggregate85 and whole-Git85 requirements remain blocking until their
own actual required checks pass. Future #1750 formal integration must normally
incorporate the accepted security baseline and recheck all-state duplicate and
overlapping PR heads. #291 is permanent and never a closing directive.
