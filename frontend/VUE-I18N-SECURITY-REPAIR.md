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

## Lock generation and evidence status

This initial source commit intentionally retains the existing lockfile bytes.
It is **not installable frozen-source acceptance evidence** until remote CI has
generated the new lock, its complete diff has been independently reviewed, and
the exact returned bytes have been imported into a new frozen source commit.
No local or server 87 package manager, formatter, test or compiler may run.

A separately reviewed CI controller must use this exact full Git tree, Node 22
and pnpm 9.15.9 to generate the lock without executing lifecycle scripts. It
must archive complete before/after source identity, actual command/exit/stdout/
stderr, tool versions, the original and generated lock bytes, their diff and
hashes, and prove that only `frontend/pnpm-lock.yaml` changed. Unexpected lock
churn or source changes are review findings, not permission to refresh unrelated
dependencies. Preserve the existing DOMPurify and follow-redirects repairs.

After import, actual remote frozen installation, named ten-case and full
frontend suites, typecheck, locale/key checks, production build and unchanged
security policy must pass on the exact new head. Inspect actual exits and all
named assertions; a green job with a continued test failure is insufficient.
Keep raw audit output, production dependency paths and original exceptions.
No new exception, skip, lowered gate or synthetic test PASS is acceptable.

Original Go 880 identities, all 132 original source provenance rows, all 17
production per-file 85% gates, both original 85% gates and four original
profiles remain required. This source declaration is NOTRUN and is not LGTM.
