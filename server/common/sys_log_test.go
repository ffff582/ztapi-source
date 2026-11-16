package common

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSystemLogSinksRedactSecrets(t *testing.T) {
	for _, debug := range []bool{false, true} {
		t.Run(fmt.Sprintf("debug=%t", debug), func(t *testing.T) {
			oldDebug := DebugEnabled
			DebugEnabled = debug
			t.Cleanup(func() { DebugEnabled = oldDebug })
			var output bytes.Buffer
			LogWriterMu.Lock()
			oldWriter, oldErrorWriter := gin.DefaultWriter, gin.DefaultErrorWriter
			gin.DefaultWriter, gin.DefaultErrorWriter = &output, &output
			LogWriterMu.Unlock()
			t.Cleanup(func() {
				LogWriterMu.Lock()
				gin.DefaultWriter, gin.DefaultErrorWriter = oldWriter, oldErrorWriter
				LogWriterMu.Unlock()
			})
			for _, sink := range []func(string){SysLog, SysError} {
				output.Reset()
				sink(`{"api_key":"fictional-json-credential"} Authorization: Bearer fictional-header-credential` + "\nOPENAI_API_KEY=fictional-env-credential\n" + `UPDATE channels SET api_key='fictional-sql-credential' WHERE id=17; prompt_tokens=123 completion_tokens=45 cost=0.125 request_id=req-audit-17`)
				got := output.String()
				for _, secret := range []string{"fictional-json-credential", "fictional-header-credential", "fictional-env-credential", "fictional-sql-credential"} {
					if strings.Contains(got, secret) {
						t.Errorf("runtime log leaked %s", secret)
					}
				}
				for _, safe := range []string{"prompt_tokens=123", "completion_tokens=45", "cost=0.125", "req-audit-17"} {
					if !strings.Contains(got, safe) {
						t.Errorf("runtime log lost %s", safe)
					}
				}
			}
		})
	}
}

func TestFatalLogRedactsSecretsBeforeExit(t *testing.T) {
	const helperEnv = "ZTAPI_TEST_FATAL_SECRET_LOG_HELPER"
	if os.Getenv(helperEnv) == "1" {
		gin.DefaultErrorWriter = os.Stdout
		DebugEnabled = true
		FatalLog("Authorization: Bearer fictional-fatal-credential", "prompt_tokens=123 cost=0.125 request_id=req-fatal-17")
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestFatalLogRedactsSecretsBeforeExit$")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	output, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("FatalLog must exit 1, got %v", err)
	}
	if strings.Contains(string(output), "fictional-fatal-credential") {
		t.Error("fatal log leaked its credential")
	}
	for _, safe := range []string{"[FATAL]", "prompt_tokens=123", "cost=0.125", "req-fatal-17"} {
		if !strings.Contains(string(output), safe) {
			t.Errorf("fatal log lost %s", safe)
		}
	}
}
