# Existing #1712 decoder dependency contracts

RAW source only. These tools have not been executed, syntax checked, formatted,
or tested locally or on server 87. All execution must use remote CI and an exact
reviewed Git head. No whole-PR LGTM or runtime acceptance is claimed.

The wrapper requires Node 22 and pnpm 9.15.9, a raw event bound to
`EXPECTED_SOURCE_HEAD`, and a fresh external evidence directory:

```sh
python3 -B tools/verify_decode_uri_installation.py \
  --expected-head "$EXPECTED_SOURCE_HEAD" --evidence "$RUNNER_TEMP/decode-evidence"
```

It records raw before/after bytes, Git modes/index/status and every tracked blob.
Installed modules stay Git ignored. NODE_OPTIONS and NODE_PATH must be absent.
Actual package/lock hashes are preserved; the scoped query-string 9.3.1 decoder
override must be exactly 0.5.1. It preserves actual Node command streams/exits,
owner manifests, the official SRI-pinned tarball, and all five published files
for every physical decoder instance. No installation or dependency change is
performed by either tool.

The Node probe follows real owner contexts from frontend through icons 4.0.2,
UI 4.9.2, query-string 9.3.1 and decode-uri-component 0.5.1. It actually imports
the installed decoder and query-string ESM entries. Reading icons/UI manifests
does not execute those packages or prove that application code passes attacker
input to them. Eight exact names must each emit one RUN and one PASS:

| Name | Actual contract |
| --- | --- |
| decode.valid-ascii-utf8 | Native parity and literal Unicode/control-byte goldens |
| decode.malformed-bom | Malformed percent/UTF-8, uppercase truncated C2 and BOM goldens |
| decode.single-pass | One-level decode and percent separator preservation |
| query.plus-space-encoded-plus | Query plus-to-space, encoded plus and single-pass percent |
| query.duplicates-null-empty-roundtrip | Duplicate values, null/empty distinction, literal serialization and parse roundtrip |
| query.decode-false-numbers-booleans | Decode-disabled bytes and actual numeric/boolean options |
| query.malformed-tail | Malformed, BOM and once-encoded values through real query-string |
| decode.publisher-distinct-runs-32000 | Exact publisher input construction, restricted-domain complete output oracle, query parity and natural owned-child completion |

The last group preserves its actual input/output, timings, child streams and
exit/signal. A ten-second watchdog, launch failure, abnormal exit, missing group
or stream, or incomplete cleanup is a failure. This is a bounded regression
observation, not an asymptotic complexity proof or an OLD benchmark. Its lowercase
hex payload preserves lowercase orphan bytes; the publisher's special uppercase
`%C2` behavior is covered separately and is not silently generalized.

Primary contract sources:

- https://github.com/SamVerschueren/decode-uri-component/blob/v0.5.1/test.js
- https://github.com/SamVerschueren/decode-uri-component/blob/v0.5.1/index.js
- https://github.com/sindresorhus/query-string/blob/v9.3.1/base.js
- https://github.com/sindresorhus/query-string/blob/v9.3.1/readme.md

The wrapper's package bytes and this narrow eight-group regression cannot replace
Vue tests, build/typecheck, the unchanged audit exception policy, Go/race/Windows
checks, four original formal coverage profiles, or either original 85% gate.
