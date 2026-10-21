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
# Actual cold/warm graphs and version/sum/replacement negative controls run in CI.
python3 -B "$backend/tools/req-http2-security-evidence_test.py" > "$evidence/old-graph-regressions.stdout" 2> "$evidence/old-graph-regressions.stderr"
go version | tee "$evidence/go-version.txt"
grep -q 'go1.26.9 ' "$evidence/go-version.txt"
# New private H3 contract sources are formatted and checked only in remote CI.
gofmt -l third_party/req/internal/http3/frame_contract_test.go third_party/req/internal/http3/header_contract_test.go third_party/req/internal/http3/request_writer_contract_test.go > "$evidence/http3-contract-gofmt.list"
test ! -s "$evidence/http3-contract-gofmt.list"
gofmt -l third_party/req/http3_wire_peer_test.go third_party/req/http3_wire_contract_test.go third_party/req/http3_wire_cancel_test.go third_party/req/http3_wire_tls_test.go third_party/req/http3_wire_decompression_test.go third_party/req/internal/http3/tls_config_contract_test.go third_party/req/internal/http3/transport.go third_party/req/internal/http3/stream.go third_party/req/internal/compress/deflate_reader.go third_party/req/internal/compress/deflate_close_test.go > "$evidence/http3-wire-gofmt.list"
test ! -s "$evidence/http3-wire-gofmt.list"
# Official meta charset repair and its actual HTTP contracts are checked only in remote CI.
gofmt -l third_party/req/decode.go third_party/req/meta_charset_contract_test.go > "$evidence/meta-charset-gofmt.list"
test ! -s "$evidence/meta-charset-gofmt.list"
# Reviewed F44 and child1743 ownership sources are checked only in remote CI.
gofmt -l third_party/req/client_behavior_peer_test.go third_party/req/client_behavior_observer_test.go third_party/req/client_behavior_contract_test.go third_party/req/client_redirect_cookie_contract_test.go third_party/req/client_response_contract_test.go third_party/req/digest_ownership_peer_test.go third_party/req/digest_ownership_contract_test.go third_party/req/digest.go > "$evidence/digest-client-contract-gofmt.list"
test ! -s "$evidence/digest-client-contract-gofmt.list"
# New private H2 frame contract source is checked only in remote CI.
gofmt -l third_party/req/internal/http2/frame_contract_test.go > "$evidence/http2-frame-contract-gofmt.list"
test ! -s "$evidence/http2-frame-contract-gofmt.list"
# New private HTTP1 transfer source is checked only in remote CI.
gofmt -l third_party/req/transfer_contract_test.go > "$evidence/http1-transfer-contract-gofmt.list"
test ! -s "$evidence/http1-transfer-contract-gofmt.list"
# New private H2 transport wire sources are checked only in remote CI.
gofmt -l third_party/req/internal/http2/transport_contract_peer_test.go third_party/req/internal/http2/transport_contract_test.go > "$evidence/http2-transport-contract-gofmt.list"
test ! -s "$evidence/http2-transport-contract-gofmt.list"
go list -m -json all > "$evidence/modules.json"
go list -json "$module/..." > "$evidence/packages.json"
python3 "$checker" packages "$backend" "$evidence"
gofmt -d third_party/req/internal/http2/{flow,transport,frame}.go third_party/req/internal/http2/{flow,transport,frame}_security_test.go third_party/req/transport_security_test.go third_party/req/internal/dump/{dump.go,dump_concurrency_test.go} third_party/req/{request,response,trace,dump_concurrency_test,trace_concurrency_test,http1_lifecycle_test,http1_upload_test,proxy_lifecycle_test}.go > "$evidence/gofmt.diff"
test ! -s "$evidence/gofmt.diff"
# -p bounds compiler parallelism; -parallel bounds tests within each package.
go test -p=1 -parallel=1 -race -count=1 -timeout=10m -json -run '^(TestReqHTTP2Security_|TestDumper|TestReqDumpSecurity_|TestReqTraceSecurity_|TestReqHTTP1Lifecycle_|TestReqProxyLifecycle_|TestReqHTTP3Contract_|TestReqHTTP3Wire_|TestReqHTTP3PublicTLS_|TestReqHTTP3TLSControl_|TestReqHTTP3Decompression_|TestReqHTTP3DeflateClose_|TestReqDeflateClose_|TestReqHTTP2FrameContract_|TestReqHTTP1Transfer_|TestReqHTTP2TransportContract_|TestReqMetaCharset_|TestReqClientDigest_|TestReqClientRedirect_|TestReqClientCookie_|TestReqClientResponse_|TestReqDigestOwnership_)' -covermode=atomic -coverpkg="$module/..." -coverprofile="$evidence/named.raw.out" "$private" "$module/internal/dump" "$module" "$module/internal/http3" "$module/internal/compress" > "$evidence/named.json" 2> "$evidence/named.stderr"
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
