# Vue I18n security repair in existing PR #1712

The user authorized repair of the actual GHSA-83rm-6c8r-g728 frontend-security
failure within existing PR #1712 on 2026-10-30. Permanent tracking: #291.
No separate issue or PR is opened, and no audit exception is added.

## Dependency and environment scope

The proposed direct dependency is pinned to the official patched Vue I18n
11.4.14. Its official npm metadata requires Node >=22 and accepts Vue ^3.0.0;
the fork already uses Vue 3.5.43. The five frontend Node declarations in the
existing three workflows use Node 22. Docker's existing Node 24 remains suitable.
Other dependency upgrades, translation text, initialization, loading/fallback,
Vite/Vitest aliases, JIT settings and business behavior are outside this change.

The official 11.4.14 package still includes the old runtime ES module alias as
a compatibility re-export. The original alias therefore does not need to be
rewritten solely because the new implementation uses `.runtime.mjs`.

Sources:

- Advisory: <https://github.com/advisories/GHSA-83rm-6c8r-g728>
- Release: <https://github.com/intlify/vue-i18n/releases/tag/v11.4.14>
- Official metadata: <https://registry.npmjs.org/vue-i18n/11.4.14>
- Official source commit: `6bb9ebdeae4955cafb53e82bc461199f6186f573`
- v10 migration: <https://vue-i18n.intlify.dev/guide/migration/breaking10>
- v11 migration: <https://vue-i18n.intlify.dev/guide/migration/breaking11>

The advisory's conditions include translated HTML and the relevant escaping
path. The observed three vulnerable installed packages and failed audit gate
are not proof that this application's current configuration is exploitable.
The package regression below uses harmless HTML in an isolated composer.

## Real translation regressions

`vueI18nSecurity.spec.ts` adds ten independently required cases listed in
`tools/vue-i18n-security-inventory.json`:

- Three actual application lazy loaders render literal Chinese/English text and
  named interpolation. Repeated loading installs the same locale only once.
- The actual global and component composers render and update after a language
  switch. Vue I18n, the locale resources and the title translator are not mocked.
- Independent literal resources verify nested fallback precedence, preferred
  messages and real interpolation through the application completion function.
- A stored legacy `zh` preference migrates to `zh-CN` and renders Chinese text.
- Explicit locale changes persist the preference and use the real translated
  route title. Only router/store inputs are isolated.
- Fresh message interpolation runs with dynamic `Function` construction denied
  in both host and window realms. A deliberate constructor call must fail,
  proving that the test tripwire is active. Original descriptors are restored.
  This is a controlled JIT contract, not proof of browser response CSP headers.
- Literal and named harmless iframe messages pass through the actual
  `escapeParameter` sanitizer. Parsing the returned HTML must retain the iframe
  and its ordinary title while removing the active `srcdoc` attribute. This
  tests the security behavior without executing an attack payload.

All tests are sequential. Components unmount, isolated composers dispose, and
storage, module input mocks and global Function descriptors are restored.
The existing runtime/locale/key tests remain unchanged. A message object's
having a key cannot substitute for an actual translated-output assertion.

## Lock generation, reconciliation and evidence status

Remote lock-only generation completed in run `40993653992`, artifact
`12950151961`, using exact source `88aac98317b0192b3e05204af009d89a3dc2bb3a`
and controller `320eac8be24770bb3600e444662c0ca98b926446`. Actual Node
22.23.4 and pnpm 9.15.9 ran `pnpm install --lockfile-only --ignore-scripts
--no-frozen-lockfile` with an isolated owned store. The actual command exited
zero; complete Git snapshots and three source/controller archives established
that only the lock changed. This generated dependency data, not application
test or security acceptance. No local or server 87 project execution occurred.

The imported lock is a **metadata-reconciled derivative**, not the unmodified
pnpm artifact. Raw output removed eleven existing Rollup 4.54.0 `libc`
selectors and refreshed three unrelated `deprecated` fields. After independent
review, those fourteen complete package metadata blocks were restored from the
original lock, retaining their exact original platform/integrity/engine/peer
metadata. All forty-four unrelated direct selections, common package metadata
and common snapshot dependency edges remain byte-identical to the original.
The Vue importer and official 11.4.14 family nodes/edges retain the remote
pnpm output. Existing DOMPurify and follow-redirects repairs are preserved.

Immutable evidence identities (SHA-256):

- Original lock, 334082 bytes:
  `ee70bcdbe28e4ba9af148199e0829e5b0ff021fe66c3a74ad9629004a8d4caeb`.
- Artifact ZIP, 45886172 bytes:
  `6b409923d9e260bd7cbeb4e354c89d9e24f0f93ca79f7ac17fd4b0bfe9734850`.
- Raw generated lock, 334817 bytes:
  `965ca1c61173339d2e82fb6070f888e94735666adb5493d29229fe77eb1783c8`.
- Imported reconciled lock, 334495 bytes:
  `82ef5a10dccd9ba11d6c58b9d267bd7ff4da9b1f63a3467175f1ef35c317a72b`.
- Independent reconciliation review:
  `077fc474fc18871fd8455701f2533ace4e1e21125f3121c2bec691e35d76c80c`.

Raw generation evidence, full original/generated maps, exact fourteen-block
restoration diff and derivation receipt are retained under the task evidence
folder `vue-i18n-security-oct30/remote-lock-40993653992/`. Restoring old warning
text does not repair or prove the safety of those unrelated packages; raw new
warnings remain preserved. No audit policy or advisory exception changes.

Frozen installation of these derived bytes and all application execution are
**NOTRUN**. Remote Node 22/pnpm 9.15.9 frozen installation must succeed without
rewriting this lock. Any rejected or rewritten selector remains a finding;
lock generation success cannot replace that evidence.

Actual remote frozen installation, named ten-case and full
frontend suites, typecheck, locale/key checks, production build and unchanged
security policy must pass on the exact new head. Inspect actual exits and all
named assertions; a green job with a continued test failure is insufficient.
Keep raw audit output, production dependency paths and original exceptions.
No new exception, skip, lowered gate or synthetic test PASS is acceptable.

Original Go 880 identities, all 132 original source provenance rows, all 17
production adaptations plus the newly authorized eighteenth bisect adaptation
under the same per-file 85% algorithm, both original 85% gates and four original
profiles remain required. This source declaration is NOTRUN and is not LGTM.

## Permanent exact-case CI registration

The existing strict frontend-fixture job retains its original ten files and
twenty-seven required names, then adds this eleventh file and ten exact Vue
identities. Both named and full actual Vitest JSON must contain the complete
suite-prefixed fullName multiset once each with passed status. Dynamic strings
retain Vitest 2.1.9's quotes; no quote stripping or suffix matching substitutes
for the Vue exact-case check. The original single todo remains the sole allowed
nonpassed assertion in the full suite, and the minimum count rises by ten.

That job now records Node22/pnpm9.15.9, frozen-install streams/exit and unchanged
lock bytes, a separate blocking typecheck with streams/exit, and all tracked
source hashes before/after execution. Other frontend/build/security jobs retain
their checks. This registers requirements; actual execution remains NOTRUN.
