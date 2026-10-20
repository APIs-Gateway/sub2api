#!/usr/bin/env bash
# Execute only in CI or an authorised isolated remote validation container.
set -euo pipefail
backend=$(pwd -P)
repo=$(git rev-parse --show-toplevel)
evidence=${REQ_SECURITY_EVIDENCE:?Set a fresh owned evidence directory outside the checkout}
test ! -e "$evidence"
mkdir -p "$evidence"
export GOTOOLCHAIN=local GOWORK=off
export GOMAXPROCS="${REQ_SECURITY_GOMAXPROCS:-2}"
case "$GOMAXPROCS" in 1|2) ;; *) exit 2 ;; esac
export GOFLAGS=-mod=readonly
checker="$backend/tools/req-http2-security-evidence.py"
module=github.com/imroc/req/v3
private="$module/internal/http2"
git rev-parse HEAD > "$evidence/head.txt"
test "$(cat "$evidence/head.txt")" = "${REQ_SECURITY_EXPECTED_HEAD:?Exact frozen source head required}"
git diff --exit-code HEAD -- > "$evidence/checkout.diff"
git ls-files --stage > "$evidence/git-index.txt"
python3 "$checker" inputs "$repo" "$evidence"
go version | tee "$evidence/go-version.txt"
grep -q 'go1.26.9 ' "$evidence/go-version.txt"
go list -m -json all > "$evidence/modules.json"
go list -json "$module/..." > "$evidence/packages.json"
python3 "$checker" packages "$backend" "$evidence"
gofmt -d third_party/req/internal/http2/{flow,transport,frame}.go third_party/req/internal/http2/{flow,transport,frame}_security_test.go third_party/req/transport_security_test.go third_party/req/internal/dump/{dump.go,dump_concurrency_test.go} third_party/req/{request,response,trace,dump_concurrency_test,trace_concurrency_test}.go > "$evidence/gofmt.diff"
test ! -s "$evidence/gofmt.diff"
# -p bounds compiler parallelism; -parallel bounds tests within each package.
go test -p=1 -parallel=1 -race -count=1 -timeout=10m -json -run '^(TestReqHTTP2Security_|TestDumper|TestReqDumpSecurity_|TestReqTraceSecurity_)' -covermode=atomic -coverpkg="$module/..." -coverprofile="$evidence/named.raw.out" "$private" "$module/internal/dump" "$module" > "$evidence/named.json" 2> "$evidence/named.stderr"
python3 "$checker" named "$backend" "$evidence"
# Wildcard deliberately omits the original internal/testdata helper suite.
# Its cert_test.go lacks an io import; retain both original files verbatim.
go test -p=1 -parallel=1 -race -count=1 -timeout=15m -json -covermode=atomic -coverpkg="$module/..." -coverprofile="$evidence/all.raw.out" "$module/..." > "$evidence/all.json" 2> "$evidence/all.stderr"
python3 "$checker" all "$backend" "$evidence"
python3 "$checker" original "$backend" "$evidence"
# Separate modfile: only the replacement path changes; same secured main MVS.
cp go.mod "$evidence/old.mod"
cp go.sum "$evidence/old.sum"
go mod edit -modfile="$evidence/old.mod" -replace="$module=$evidence/original"
go list -modfile="$evidence/old.mod" -m -json all > "$evidence/old-modules.json"
python3 "$checker" old-graph "$backend" "$evidence"
cp third_party/req/internal/http2/transport_security_test.go "$evidence/original/internal/http2/transport_security_test.go"
set +e
go test -modfile="$evidence/old.mod" -p=1 -parallel=1 -count=1 -timeout=2m -json -run '^TestReqHTTP2Security_LazySettingsOverflowGOAWAY$' "$private" > "$evidence/old-red.json" 2> "$evidence/old-red.stderr"
old_status=$?
set -e
printf '%s\n' "$old_status" > "$evidence/old-red.exit"
python3 "$checker" old-red "$backend" "$evidence"
# Raw files remain untouched. Mapped files change paths only, including zeroes.
python3 "$checker" coverage "$backend" "$evidence"
cp "$evidence/all.mapped.out" "$backend/coverage-req-http2.out"
git diff --exit-code HEAD -- > "$evidence/checkout-after.diff"
