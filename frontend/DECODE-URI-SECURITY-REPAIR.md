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
original CI gate. It adds no advisory suppression. The lock must be generated
on remote CI with Node 22 and pnpm 9.15.9 and reviewed against the frozen full
source tree; this initial source commit deliberately retains the old lock and
cannot pass a frozen installation by itself.

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
