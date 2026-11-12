package common

import (
	"strings"
	"testing"
)

func TestMaskSensitiveInfoRemovesCredentialShapes(t *testing.T) {
	const marker = "AUDIT_SENTINEL_PRIVATE_VALUE_0123456789"
	for _, input := range []string{
		"api_key=" + marker,
		`{"api_key":"` + marker + `","input_tokens":1234,"cost":0.0373}`,
		"Authorization: Bearer " + marker,
		`{"Authorization":"Bearer ` + marker + `"}`,
		"API_KEY: " + marker,
		"ZTAPI_UPSTREAM_MASTER_KEY=" + marker,
		"OPENAI_API_KEY='" + marker + "'",
		"SQL_DSN=user:" + marker + "@tcp(mysql:3306)/ztapi",
		"REDIS_CONN_STRING=redis://default:" + marker + "@redis:6379",
		"password = \"" + marker + "\"",
		"Cookie: session=" + marker + "; preference=light",
		"Set-Cookie: refresh=" + marker + "; HttpOnly; Secure",
		"upstream " + `sk-test-` + marker,
	} {
		t.Run(input[:min(len(input), 24)], func(t *testing.T) {
			got := MaskSensitiveInfo(input)
			if strings.Contains(got, marker) {
				t.Fatalf("credential was not removed for input class: %q", input[:min(len(input), 24)])
			}
		})
	}
}

func TestLocalLogPreviewRedactsBeforeTruncationAndInDebug(t *testing.T) {
	old := DebugEnabled
	defer func() { DebugEnabled = old }()
	const marker = "AUDIT_SENTINEL_PRIVATE_VALUE_0123456789"
	for _, debug := range []bool{false, true} {
		DebugEnabled = debug
		input := strings.Repeat("x", 2010) + " api_key=" + marker + strings.Repeat("z", 200)
		out := LocalLogPreview(input)
		if strings.Contains(out, "AUDIT_SENTINEL") {
			t.Fatalf("partial secret escaped before truncation, debug=%v", debug)
		}
	}
}

func TestCredentialBoundaryRetainsBillingAndRequestEvidence(t *testing.T) {
	input := `{"request_id":"0123456789abcdef0123456789abcdef","input_tokens":1234,"output_tokens":56,"cache_read_tokens":0,"cost":0.0373,"model":"zt-claude-opus-5"}`
	if got := MaskSensitiveInfo(input); got != input {
		t.Fatalf("non-secret billing evidence changed: %s", got)
	}
}

func TestSecretBoundaryCoversBearerAndSignedURLsWithoutLosingLedgerIDs(t *testing.T) {
	for _, raw := range []string{
		"upstream rejected Bearer opaque-auth-credential request_id=req-safe-17",
		"https://example.invalid/object?X-Amz-Signature=opaque-auth-credential&request_id=req-safe-17",
	} {
		clean := RedactSecrets(raw)
		if strings.Contains(clean, "opaque-auth-credential") || !strings.Contains(clean, "req-safe-17") {
			t.Fatalf("unsafe or missing operational ID: %q", clean)
		}
	}
	for _, raw := range []string{`idempotency_key=settlement-charge-17`, `{"idempotency_key":"settlement-charge-17"}`} {
		if !strings.Contains(RedactSecrets(raw), "settlement-charge-17") {
			t.Fatalf("ledger ID removed: %q", RedactSecrets(raw))
		}
	}
}

func TestCredentialRedactionIsIdempotentAndKeepsStructuredEvidence(t *testing.T) {
	for _, input := range []string{
		"api_key=AUDIT_SENTINEL_PRIVATE_VALUE",
		`{"api_key":{"nested":"AUDIT_SENTINEL_PRIVATE_VALUE"},"input_tokens":1234567890123456789,"cost":0.0373}`,
		`{"api_key":["AUDIT_SENTINEL_PRIVATE_VALUE"],"request_id":"0123456789abcdef"}`,
	} {
		once := RedactSecrets(input)
		if twice := RedactSecrets(once); twice != once {
			t.Fatalf("not idempotent: %s", twice)
		}
		if strings.Contains(once, "AUDIT_SENTINEL") {
			t.Fatal("nested credential escaped")
		}
		if strings.HasPrefix(input, "{") {
			var value any
			if err := DecodeJsonStrict(strings.NewReader(once), &value); err != nil {
				t.Fatalf("evidence JSON was damaged: %s", once)
			}
			if strings.Contains(input, "1234567890123456789") && !strings.Contains(once, "1234567890123456789") {
				t.Fatal("large usage integer lost precision")
			}
		}
	}
}

func TestSecretBoundaryRedactsEmbeddedStructuredCredentials(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		safe  []string
	}{
		{"prefixed_object", `body={"api_key":{"a":"FICTIONAL_EMBEDDED_A","b":"FICTIONAL_EMBEDDED_B"},"input_tokens":1234567890123456789,"cost":0.0373,"request_id":"req-17","idempotency_key":"charge-19"} tail=req-23`, []string{"1234567890123456789", "0.0373", "req-17", "charge-19", "req-23"}},
		{"prefixed_environment", `upstream={"environment":{"ARBITRARY_NAME":"FICTIONAL_EMBEDDED_A","SECOND_NAME":["FICTIONAL_EMBEDDED_B"]},"request_id":"req-17"}`, []string{"req-17"}},
		{"array_environment", `upstream={"env":[{"UNKNOWN_NAME":"FICTIONAL_EMBEDDED_A"},["FICTIONAL_EMBEDDED_B"]],"input_tokens":123}`, []string{"123"}},
		{"assigned_environment", `environment=[{"UNKNOWN_NAME":"FICTIONAL_EMBEDDED_A"},["FICTIONAL_EMBEDDED_B"]] request_id=req-17 cost=0.0373`, []string{"req-17", "0.0373"}},
		{"assigned_credential", `api_key={"a":"FICTIONAL_EMBEDDED_A","b":"FICTIONAL_EMBEDDED_B"} request_id=req-17`, []string{"req-17"}},
		{"unquoted_container", `body={api_key:{a:FICTIONAL_EMBEDDED_A,b:FICTIONAL_EMBEDDED_B},input_tokens:123,request_id:req-17}`, []string{"123", "req-17"}},
		{"nested_escaped_json", `body={"message":"{\"api_key\":{\"a\":\"FICTIONAL_EMBEDDED_A\",\"b\":\"FICTIONAL_EMBEDDED_B\"},\"input_tokens\":1234567890123456789}","idempotency_key":"charge-19"}`, []string{"1234567890123456789", "charge-19"}},
		{"quoted_escaped_json", `body="{\"env\":{\"UNKNOWN\":[\"FICTIONAL_EMBEDDED_A\",\"FICTIONAL_EMBEDDED_B\"]},\"request_id\":\"req-17\"}" cost=0.0373`, []string{"req-17", "0.0373"}},
		{"multiple_segments", `first={"api_key":{"a":"FICTIONAL_EMBEDDED_A","b":"FICTIONAL_EMBEDDED_B"}} middle req-17 env=["FICTIONAL_EMBEDDED_C","FICTIONAL_EMBEDDED_D"] last={"input_tokens":1234567890123456789,"cost":0.0373,"idempotency_key":"charge-19"}`, []string{"req-17", "1234567890123456789", "0.0373", "charge-19"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			clean := RedactSecrets(test.input)
			if strings.Contains(clean, "FICTIONAL_EMBEDDED_") {
				t.Fatal("embedded structured credential escaped")
			}
			for _, safe := range test.safe {
				if !strings.Contains(clean, safe) {
					t.Errorf("lost safe evidence %s", safe)
				}
			}
			if twice := RedactSecrets(clean); twice != clean {
				t.Fatalf("embedded redaction is not idempotent: %q", twice)
			}
		})
	}
}

func TestSecretBoundaryPreservesOrdinaryEmbeddedPayloads(t *testing.T) {
	for _, input := range []string{
		`[INFO] payload={ "messages": [{"role":"user","content":"ordinary [text] {braces}"}], "data":{"a":"A","b":"B"}, "input_tokens":1234567890123456789, "cost":0.0373, "idempotency_key":"charge-19" } req-17`,
		`first=[1,2,3] second={"response":{"output":[],"error":null}} request_id=req-17`,
		`body="{\"data\":{\"a\":\"A\",\"b\":\"B\"},\"idempotency_key\":\"charge-19\"}"`,
		`not JSON [plain text] {plain words} request_id=req-17`,
	} {
		if clean := RedactSecrets(input); clean != input {
			t.Fatalf("ordinary payload changed: %q", clean)
		}
	}
}

func TestSecretBoundaryRedactsEmbeddedStructureBeforeTruncation(t *testing.T) {
	previousDebug := DebugEnabled
	t.Cleanup(func() { DebugEnabled = previousDebug })
	input := strings.Repeat("p", 1980) + ` body={"api_key":{"a":"` + strings.Repeat("FICTIONAL_EMBEDDED_A", 200) + `","b":"FICTIONAL_EMBEDDED_B"},"request_id":"req-17"}`
	for _, debug := range []bool{false, true} {
		DebugEnabled = debug
		if clean := LocalLogPreview(input); strings.Contains(clean, "FICTIONAL") {
			t.Fatalf("embedded credential survived truncation boundary, debug=%t", debug)
		}
	}
}

func TestSecretBoundaryBoundsEmbeddedJSONResources(t *testing.T) {
	deep := strings.Repeat(`{"next":`, 256) + `{"api_key":{"a":"FICTIONAL_EMBEDDED_A","b":"FICTIONAL_EMBEDDED_B"}}` + strings.Repeat("}", 256)
	large := `{"padding":"` + strings.Repeat("x", maxSecretJSONBytes) + `","env":{"UNKNOWN":"FICTIONAL_EMBEDDED_A"}}`
	for _, fragment := range []string{deep, large} {
		clean := RedactSecrets("body=" + fragment + " request_id=req-resource-17 cost=0.0373")
		if strings.Contains(clean, "FICTIONAL_EMBEDDED_") {
			t.Fatal("over-budget sensitive fragment escaped")
		}
		if !strings.Contains(clean, "req-resource-17") || !strings.Contains(clean, "cost=0.0373") {
			t.Fatal("over-budget redaction removed surrounding operational evidence")
		}
		if RedactSecrets(clean) != clean {
			t.Fatal("over-budget redaction is not idempotent")
		}
	}
	ordinary := `body={"padding":"` + strings.Repeat("x", maxSecretJSONBytes) + `","input_tokens":1234567890123456789,"idempotency_key":"charge-19"}`
	if RedactSecrets(ordinary) != ordinary {
		t.Fatal("ordinary large payload changed")
	}
}

func TestSecretBoundaryExactPluralContainers(t *testing.T) {
	for _, field := range []string{"credentials", "secrets", "CREDENTIALS", "SeCrEtS"} {
		for _, container := range []string{
			`{"a":"OPAQUE_PLURAL_A","b":"OPAQUE_PLURAL_B"}`,
			`[{"unknown":"OPAQUE_PLURAL_A"},"OPAQUE_PLURAL_B"]`,
		} {
			input := `body={"` + field + `":` + container + `,"input_tokens":1234567890123456789,"cost":0.125,"request_id":"req-plural-17","idempotency_key":"charge-19"}`
			clean := RedactSecrets(input)
			if strings.Contains(clean, "OPAQUE_PLURAL_") {
				t.Errorf("opaque %s container escaped", field)
			}
			for _, safe := range []string{"1234567890123456789", "0.125", "req-plural-17", "charge-19"} {
				if !strings.Contains(clean, safe) {
					t.Errorf("%s container redaction lost %s", field, safe)
				}
			}
			if RedactSecrets(clean) != clean {
				t.Errorf("%s container redaction is not idempotent", field)
			}
		}
	}
	deep := strings.Repeat(`{"next":`, maxSecretJSONDepth+1) + `{"credentials":{"unknown":"OPAQUE_PLURAL_A"}}` + strings.Repeat("}", maxSecretJSONDepth+1)
	if strings.Contains(RedactSecrets("body="+deep+" request_id=req-plural-17"), "OPAQUE_PLURAL_") {
		t.Error("over-budget plural container escaped")
	}
	ordinary := `body={"customer_credentials":{"a":"public"},"business-secrets":{"b":"public"},"credentials_id":"audit-19","secrets_count":7,"input_tokens":123,"idempotency_key":"charge-19"}`
	if RedactSecrets(ordinary) != ordinary {
		t.Error("non-exact business fields changed")
	}
}
