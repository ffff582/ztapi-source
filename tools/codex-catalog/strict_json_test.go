package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMetadataRejectsDuplicateFields(t *testing.T) {
	for _, input := range []string{
		`{"slug":"zt-claude-opus-5","SLUG":null,"base_instructions":"full"}`,
		`{"slug":"zt-claude-opus-5","BASE_INSTRUCTIONS":"full"}`,
		`{"slug":"zt-claude-opus-5","slug":null,"base_instructions":"full"}`,
		`{"slug":"zt-claude-opus-5","base_instructions":"full","base_instructions":null}`,
		`{"slug":"zt-claude-opus-5","base_instructions":"full","future":{"a":1,"a":2}}`,
	} {
		path := filepath.Join(t.TempDir(), "model.json")
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadModelInfo(path); err == nil {
			t.Fatal("ambiguous metadata accepted")
		}
	}
}

func TestUpstreamDuplicateFieldsFailClosed(t *testing.T) {
	for _, body := range []string{
		`{"data":[{"ID":"zt-claude-opus-5","id":null}]}`,
		`{"DATA":[{"id":"zt-claude-opus-5"}]}`,
		`{"data":[{"ID":"zt-claude-opus-5"}]}`,
		`{"data":[{"id":"zt-claude-opus-5","id":null}]}`,
		`{"data":[],"data":[{"id":"zt-claude-opus-5"}]}`,
		`{"data":[{"id":"zt-claude-opus-5","unknown":{"a":1,"a":2}}]}`,
	} {
		h := testCatalog(t, "", func(*http.Request) (*http.Response, error) { return upstreamResponse(200, body), nil })
		w := serve(h, catalogRequest())
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("ambiguous upstream accepted: %d", w.Code)
		}
	}
}

func TestProxyTrustRequiresExactHosts(t *testing.T) {
	for _, trust := range []string{"0.0.0.0/1", "172.20.0.0/16", "172.20.0.7/31", "fd00::/64", "2001:db8::7/127"} {
		if _, err := readConfig(testEnv(map[string]string{"TRUSTED_PROXY_CIDRS": trust})); err == nil {
			t.Fatalf("broad trust accepted: %s", trust)
		}
	}
}

func TestMetadataRejectsDeepOrTrailingJSON(t *testing.T) {
	for _, extra := range []string{strings.Repeat("[", 129) + "0" + strings.Repeat("]", 129), `{"a":1} {"b":2}`} {
		path := filepath.Join(t.TempDir(), "model.json")
		input := `{"slug":"zt-claude-opus-5","base_instructions":"full","future":` + extra + `}`
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadModelInfo(path); err == nil {
			t.Fatal("unbounded or trailing JSON accepted")
		}
	}
}
