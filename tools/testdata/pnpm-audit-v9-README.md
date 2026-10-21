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
