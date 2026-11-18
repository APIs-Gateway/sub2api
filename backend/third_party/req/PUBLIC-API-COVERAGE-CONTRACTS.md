# Public API coverage contracts for existing PR1712

Specification before implementation; source base29ea1fcb4c2366aee00d8f2a75c0b0edf3676880.
No runtime/format/coverage PASS is claimed. All executable validation is remote CI only.
Only new owned test files and this document may change in this lane; production,
original fixtures, registry, manifest, profile denominators and85 gates stay intact.

## Existing work read and retained

Read saved ae724579899d44281728a3daff15f60778178a67's
public_default_contract_test.go and PUBLIC-DEFAULT-CONTRACTS.md in full. Those six
cases already cover default replacement, escaped common/request precedence,
form+Basic+cookies, JSON success, HTTP error decoding and active cancellation.
They remain separately preserved; this lane does not rewrite or duplicate them.
Read the19-leaf public-facade-oct25 applicability/wire plan. Its Safari delegation
finding remains a concrete separate static concern: no production correction or
expectation accepting Firefox as Safari is included here. H2/H3 fingerprints and
header priority are excluded from this HTTP facade coverage lane.

## Behavioral scope and planned inventory

Names below are prospective until source is frozen. No projected line-hit number
is promised; actual four-profile/85 evidence must establish the result later.

| Planned group | Cases and independent observable oracle | API entrypoints |
| --- | --- | --- |
| Query construction | `common_multivalue`, `common_struct`, `request_multivalue`, `request_struct`, `invalid_common_preserves_valid`, `independent_builders`: real peer observes decoded repeated values, exact escaped URI where relevant, request-only overrides and a second fresh request without leaked state. Invalid struct/raw query configuration must retain the already-valid common values; no duplicate of old6 default replacement. | SetCommonQueryParams/Param(s)/FromValues/FromStruct/QueryString; SetQueryParams/AnyType/FromValues/FromStruct/QueryString/AddQueryParam(s); R/NewRequest/SetURL/SetPathParams. |
| Request entity semantics | `reader`, `bytes`, `string`, `json_literal_string`, `json_literal_bytes`, `xml_literal_string`, `xml_literal_bytes`, `xml_object`, `custom_content_type`, `get_payload_disabled_then_enabled`: real peer reads exact bounded entity and Content-Type. Literal JSON/XML whitespace stays literal; XML object is parsed independently; GET payload policy changes actual wire body. These are different input/encoding contracts, not one invocation per line. | SetBody/Bytes/String/JsonString/JsonBytes/XmlString/XmlBytes/XmlMarshal/ContentType; Disable/EnableAllowGetMethodPayload. |
| Forms and multipart | `ordered_fields`, `typed_form_values`, `common_repeated_form`, `file_reader`, `file_bytes`, `file_path_map`, `file_upload`, `missing_file`: peer independently parses encoded or multipart bytes into field order/repetitions, filename, media type and exact payload. Custom boundary is verified in actual Content-Type/raw framing. Missing-file error must carry filesystem identity and no successful peer request. | SetOrderedFormData/FormData/FormDataAnyType; SetCommonFormDataFromValues; SetFileReader/Bytes/File/Files/FileUpload; SetMultipartBoundaryFunc/EnableForceMultipart/DisableForceMultipart. |
| Result and codec boundaries | `deprecated_success_alias`, `deprecated_error_alias`, `common_error_type`, `custom_result_state`, `json_marshal_error`, `xml_marshal_error`, `json_unmarshal_error`, `xml_unmarshal_error`, `body_transformer`: typed success/error ownership and original status/body; exact sentinel codec errors (errors.Is), no request for encode failure, real arrived response for decode failure; transformer sees original bytes and preserves response identity. | SetResult/Error; SetCommonError/ErrorResult; SetResultStateCheckFunc; SetJson/XmlMarshal/Unmarshal; SetResponseBodyTransformer. |
| Middleware and policies | `wrapper_composition`, `before_error_blocks_wire`, `after_error_preserves_response`, `dial_error_identity`, `pre_canceled`, `jar_clear`, `redirect_denied`, `manual_read_and_reenable`: real request/order/body and exact sentinel cause; failed preflight has zero peer hits; common cookies disappear after clear while an explicitly supplied manual jar is preserved, as documented; denied redirect does not reach destination; manual body is consumed/closed before re-enabling auto-read. | WrapRoundTrip/Func; OnBeforeRequest/OnAfterResponse; SetDial; SetContext; SetCookieJar/GetCookies/ClearCookies; SetRedirectPolicy; Disable/EnableAutoReadResponse. |
| Retry replay and overrides | `common_fixed`, `request_custom_interval`, `request_backoff`, `request_zero_overrides_common`: real first503/later200 peer captures identical full body per attempt; interval/hook/condition assertions use actual response status/attempt and bounded callback records; request retry0 makes exactly one503 despite common retry1. No wall-clock microtiming assertions. | SetCommonRetryCount/Interval/FixedInterval/BackoffInterval/Hook/Condition, AddCommonRetryHook/Condition; request SetRetryCount/Interval/FixedInterval/BackoffInterval/Hook/Condition/AddHook/AddCondition. |
| Body visibility contracts | `client_all`, `client_headers`, `client_bodies`, `client_request_only`, `client_response_only`, `client_without_upload`, `client_without_download`; equivalent `each_*` and `request_*` scopes, plus independently drained `client_async` and explicit `request_options` output selection. Real distinctive request/response header/body tokens are independently present/absent according to the named public mode. Client all uses caller buffer and CloseDump drain; per-request uses Response.Dump. No timing sleeps, stdout capture, leaked owned file or source-field-only assertion. | EnableDumpAllTo/CommonDumpOptions/AllWithout*; EnableDumpEachRequest/EachWithout*; EnableDump/Without*/SetDumpOptions/EnableDumpTo. |
| Download destinations | `caller_writer`, `relative_output_file`: peer returns literal bytes; an io.Writer-only bytes.Buffer remains usable; no new no-close guarantee for io.Closer outputs is invented; output directory + package-level output file produces exact bytes in an owned temporary directory. Response body/EOF and cleanup errors are observed. | SetOutput, SetOutputFile, SetOutputDirectory; SetDownloadCallback/WithInterval. |

The initial groups deliberately avoid a Cartesian sweep of every method alias,
fingerprint or getter. Where aliases are included, the deprecated API's result
ownership is the compatibility contract. If the saved6 overlaps a planned case,
retain the existing case and implement only the additional assertion/axis here.

## Global state, concurrency and resource rules

Use public DefaultClient/SetDefaultClient exclusively. Save the original pointer,
register restoration before creating later resources, install a fresh owned
client per sequential subcase and never mutate/clone/close the saved client.
No t.Parallel and no concurrent default/common-option mutation. Every real peer
must read bounded complete bytes and publish its read/write errors back to the
test; handler goroutines never call Fatal. The unique peer cleanup first closes/joins the server, then reports every unconsumed
exchange and retained overflow record (including read/write errors), checks final
requests against explicit receive count, and emits PUBLIC_API_PEER_COMPLETE.
Buffer channels or atomic counts must not hide unexpected attempts/overflow. ClearCookies explicitly does not clear an externally supplied SetCookieJar jar; test this documented ownership boundary. The download path currently closes outputs implementing io.Closer, so the download buffer is io.Writer-only; the distinct Dump CloseDump no-adoption guarantee uses an observable closer.
Expected errors are separate from fixture
errors. Join/cancel all workers and drain/close manual bodies and owned dump scopes
before restoring the original default. CloseIdleConnections only on the owned
client. File removals must be checked after owner closure and remain failures.

## Execution and acceptance

Every new source leaf needs independent exact-head static review, remote gofmt,
actual named/full -race RUN/PASS and real required profiles. Source inventory is
not execution evidence. Both original85 gates,16 per-file85 gates and all existing
605 mandatory leaves/four adapted-original tests remain required; a full suite
failure cannot be waived to obtain a merge or coverage count. Root owns shared
registration/composition. No issue/PR, production change, threshold reduction or
source exclusion is authorized by this coverage work.
