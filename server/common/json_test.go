package common

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRejectDuplicateJsonObjectMembersRejectsEscapedEquivalentNames(t *testing.T) {
	tests := map[string]struct {
		raw  string
		name string
	}{
		"top level": {
			raw:  `{"version":1,"\u0076ersion":1}`,
			name: "version",
		},
		"nested object": {
			raw:  `{"rule":{"cost_usd":{},"cost_\u0075sd":{}}}`,
			name: "cost_usd",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := RejectDuplicateJsonObjectMembers(strings.NewReader(tc.raw))
			var duplicate *DuplicateJsonObjectMemberError
			require.ErrorAs(t, err, &duplicate)
			require.Equal(t, tc.name, duplicate.Name)
		})
	}
}

func TestRejectDuplicateJsonObjectMembersReportsDecodedPath(t *testing.T) {
	err := RejectDuplicateJsonObjectMembers(strings.NewReader(`{"usage":{"input_tokens":1,"input\u005ftokens":2}}`))
	require.Error(t, err)
	var duplicate *DuplicateJsonObjectMemberError
	require.ErrorAs(t, err, &duplicate)
	require.Equal(t, "input_tokens", duplicate.Name)
	require.Equal(t, []string{"usage", "input_tokens"}, duplicate.Path)
}

func TestFindDuplicateJsonObjectMembersCollectsEveryPath(t *testing.T) {
	duplicates, err := FindDuplicateJsonObjectMembers(strings.NewReader(`{"usage":{"input_tokens":1,"input_tokens":2},"request_id":"a","request_id":"b"}`))
	require.NoError(t, err)
	require.Equal(t, []DuplicateJsonObjectMemberError{
		{Name: "input_tokens", Path: []string{"usage", "input_tokens"}},
		{Name: "request_id", Path: []string{"request_id"}},
	}, duplicates)
}

func TestRejectDuplicateJsonObjectMembersReturnsFirstDuplicateBeforeLaterParseErrors(t *testing.T) {
	for _, raw := range []string{
		`{"a":1,"a":2,"broken":}`,
		`{"a":1,"a":2} {"extra":true}`,
	} {
		err := RejectDuplicateJsonObjectMembers(strings.NewReader(raw))
		var duplicate *DuplicateJsonObjectMemberError
		require.ErrorAs(t, err, &duplicate)
		require.Equal(t, "a", duplicate.Name)
	}
}

func TestJsonRawMessageToString(t *testing.T) {
	tests := []struct {
		name string
		data json.RawMessage
		want string
	}{
		{
			name: "object",
			data: json.RawMessage(`{"city":"Paris","days":0,"strict":false}`),
			want: `{"city":"Paris","days":0,"strict":false}`,
		},
		{
			name: "string",
			data: json.RawMessage(`"{\"city\":\"Paris\",\"days\":0,\"strict\":false}"`),
			want: `{"city":"Paris","days":0,"strict":false}`,
		},
		{
			name: "null",
			data: json.RawMessage(`null`),
			want: "",
		},
		{
			name: "empty",
			data: nil,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, JsonRawMessageToString(tt.data))
		})
	}
}

func TestDecodeJsonStrictRejectsUnknownNonIntegralAndMultipleValues(t *testing.T) {
	type payload struct {
		N int `json:"n"`
	}
	for _, raw := range []string{
		`{"n":1,"unknown":true}`,
		`{"n":1e0}`,
		`{"n":1} {"n":2}`,
	} {
		var value payload
		require.Error(t, DecodeJsonStrict(strings.NewReader(raw), &value))
	}
	var value payload
	require.NoError(t, DecodeJsonStrict(strings.NewReader(`{"n":1}`), &value))
	require.Equal(t, 1, value.N)
}
