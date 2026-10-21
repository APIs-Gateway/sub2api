# pnpm 9 audit report fixture

`pnpm-audit-v9-no-vulnerabilities.json` is an unmodified official pnpm v9.15.9
fixture, retained as a positive compatibility control for the actual CLI.

- Source: https://github.com/pnpm/pnpm/blob/v9.15.9/lockfile/plugin-commands-audit/test/utils/responses/no-vulnerabilities-response.json
- SHA256: `3d1a667d3f89d6760821df1b70f470b1acdba710ad18d7d315456c345173931d`
- Report types: https://github.com/pnpm/pnpm/blob/v9.15.9/lockfile/audit/src/types.ts
- JSON exit semantics: https://github.com/pnpm/pnpm/blob/v9.15.9/lockfile/plugin-commands-audit/src/audit.ts#L234-L238

In v9 JSON mode, any nonzero vulnerability total returns 1, including low-only
reports even with `--audit-level=high`. Registry failures throw by default. The
checker requires the complete supported report, its original 0/1 status, and
consistent severity presence; per-severity occurrence counts need not equal the
number of distinct advisories. Unknown formats and error containers fail closed.
High/critical exceptions continue to use the existing package/advisory/severity
and expiry policy. No registry-error ignore or new exception is introduced.

Run the CLI regression controls only in remote GitHub CI. The security workflow
preserves each real CLI outcome, raw registry report, audit exit, stderr, source
identity and policy inputs. No local or server 87 execution is permitted.

`pnpm-audit-v9-all-vulnerabilities.json` is also the unmodified official report:

- Source: https://github.com/pnpm/pnpm/blob/v9.15.9/lockfile/plugin-commands-audit/test/utils/responses/all-vulnerabilities-response.json
- SHA256: `5d8d4ac084b65b972d0a24098e3dc71925c4c7dd9e56cd68423e3c879b5bba5f`

Its 31 actions include 26 review actions without target, and no action includes
`isMajor`, despite these fields being required by the TypeScript interface. The
checker follows the actual fixture: action/module/resolves are required; target,
isMajor and depth are typed when present. All 46 resolutions contain the required
id/path/dev/optional/bundled fields. The full fixture must reach the unchanged
vulnerability policy and fail for real unexcepted high advisories, rather than
being misclassified as an invalid report. `muted` remains an array of unknown
values, matching the official interface.
