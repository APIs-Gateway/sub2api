//go:build unit

package repository

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemoveGroupFromModerationConfigPreservesOtherJSON(t *testing.T) {
	const raw = `{"group_ids":[7,9,7],"blocked_keywords":["\u0000"],"very_large_number":1e1000000,"unknown":{"nested":[true,null]}}`
	next, changed := removeGroupFromModerationConfig(raw, 7)
	require.True(t, changed)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(next), &fields))
	require.JSONEq(t, `[9]`, string(fields["group_ids"]))
	require.Equal(t, `["\u0000"]`, string(fields["blocked_keywords"]))
	require.Equal(t, `1e1000000`, string(fields["very_large_number"]))
	require.JSONEq(t, `{"nested":[true,null]}`, string(fields["unknown"]))
}

func TestRemoveGroupFromModerationConfigHandlesNullAndCaseVariants(t *testing.T) {
	const raw = `{"GROUP_IDS":[7,null],"Group_Ids":[9,7],"group_ids":[7,11],"gRoUp_IdS":null,"unknown":"keep"}`
	next, changed := removeGroupFromModerationConfig(raw, 7)
	require.True(t, changed)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(next), &fields))
	require.JSONEq(t, `[null]`, string(fields["GROUP_IDS"]))
	require.JSONEq(t, `[9]`, string(fields["Group_Ids"]))
	require.JSONEq(t, `[11]`, string(fields["group_ids"]))
	require.Equal(t, `null`, string(fields["gRoUp_IdS"]))
	require.Equal(t, `"keep"`, string(fields["unknown"]))
}

func TestRemoveGroupFromModerationConfigSkipsNullAliasBeforeSelectedGroup(t *testing.T) {
	const raw = `{"group_ids":null,"GROUP_IDS":[7]}`
	next, changed := removeGroupFromModerationConfig(raw, 7)
	require.True(t, changed)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(next), &fields))
	require.Equal(t, `null`, string(fields["group_ids"]))
	require.JSONEq(t, `[]`, string(fields["GROUP_IDS"]))
}

func TestRemoveGroupFromModerationConfigSkipsUnusableScope(t *testing.T) {
	for _, raw := range []string{
		`{"group_ids":[oops]}`,
		`null`,
		`[]`,
		`{"group_ids":"7"}`,
		`{"group_ids":null}`,
		`{"group_ids":[7,"bad"]}`,
		`{"group_ids":[7,1.0]}`,
		`{"group_ids":[7],"GROUP_IDS":["bad"]}`,
		`{"group_ids":[9],"unknown":true}`,
	} {
		next, changed := removeGroupFromModerationConfig(raw, 7)
		require.False(t, changed, raw)
		require.Empty(t, next, raw)
	}
}
