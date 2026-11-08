# Existing #1712 decode-uri-component security repair

The actual remote frontend audit reported
[GHSA-qr28-fpjm-wq7f](https://github.com/advisories/GHSA-qr28-fpjm-wq7f)
for decode-uri-component 0.4.1. The official fixed release is 0.5.1.
The installed graph is `@lobehub/icons@4.0.2 > @lobehub/ui@4.9.2 >
query-string@9.3.1 > decode-uri-component`. The audit establishes dependency
presence; an application route accepting hostile input has not been established.

This repair uses the exact pnpm selector
`query-string@9.3.1>decode-uri-component: 0.5.1`. It preserves the query-string,
icons, UI and Vue parent versions, existing security exceptions and every
original CI gate. It adds no advisory suppression. The lock was generated
on [remote CI41071904900](https://github.com/APIs-Gateway/sub2api/actions/runs/41071904900)
with Node 22 and pnpm 9.15.9 against the frozen full source tree. Raw generated
lock SHA256 is `914f432895491c80f4043045c815a87cf423994ec3bb994915469060cdfd8574`.
The reviewed derivative restores exactly fourteen unrelated original metadata
blocks (eleven Rollup libc selectors and three deprecation fields); it is not
the raw artifact. Imported SHA256 is
`b7425fb0b2ff7e644732abba66aef5f7697704a191c8caebb0a03532cf80becc`.
Every other package/snapshot block and importer remains byte identical; only
the scoped override, decoder version/integrity and query-string decoder edge
change. Frozen installation and runtime acceptance are still pending.

Acceptance requires actual installed owner-context resolution, official 0.5.1
package bytes, real valid/malformed decode and query option contracts, and a
bounded publisher regression that completes naturally. Full frontend tests,
typecheck, build and security checks must pass on the final head. Existing Go
880 identities, race and Windows resource checks, all four original profiles,
18 adaptation gates and both original global 85% gates remain required.

All executable validation is restricted to remote CI. Existing PR
[#1712](https://github.com/APIs-Gateway/sub2api/pull/1712) remains unapproved
until its concrete blockers are resolved. Permanent tracking issue
[#291](https://github.com/APIs-Gateway/sub2api/issues/291) remains open.
