package req_test

import "testing"

// This separately registered suite runs the original 23 real wire contracts in
// the test process. Original G32/Windows33 retain their child-process isolation.
// Parent counters arise only from this actual execution, never child profiles.
// A hang, race, assertion failure or timeout remains a required CI failure.
func TestReqParallelDownloadInProcessContract(t *testing.T) {
	TestReqParallelDownloadContract(t)
}
