package logger

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

func setupWriterBoundaryTest(t *testing.T, logDir string) (*bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var output, errorOutput bytes.Buffer
	oldLogDir, oldDebug, oldMode := *common.LogDir, common.DebugEnabled, gin.Mode()
	oldLogOutput, oldFlags, oldPrefix := log.Writer(), log.Flags(), log.Prefix()
	common.LogWriterMu.Lock()
	oldWriter, oldErrorWriter := gin.DefaultWriter, gin.DefaultErrorWriter
	gin.DefaultWriter, gin.DefaultErrorWriter = &output, &errorOutput
	common.LogWriterMu.Unlock()
	currentLogPathMu.Lock()
	oldPath, oldFile := currentLogPath, currentLogFile
	currentLogPath, currentLogFile = "", nil
	currentLogPathMu.Unlock()
	*common.LogDir = logDir
	log.SetOutput(&errorOutput)
	log.SetFlags(0)
	log.SetPrefix("")
	gin.SetMode(gin.DebugMode)
	t.Cleanup(func() {
		log.SetOutput(oldLogOutput)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
		common.LogWriterMu.Lock()
		gin.DefaultWriter, gin.DefaultErrorWriter = oldWriter, oldErrorWriter
		currentLogPathMu.Lock()
		if currentLogFile != nil {
			_ = currentLogFile.Close()
		}
		currentLogPath, currentLogFile = oldPath, oldFile
		currentLogPathMu.Unlock()
		common.LogWriterMu.Unlock()
		*common.LogDir, common.DebugEnabled = oldLogDir, oldDebug
		gin.SetMode(oldMode)
	})
	return &output, &errorOutput
}

func TestSetupLoggerProtectsGinRecoveryAndStandardLogWithoutLogDir(t *testing.T) {
	for _, debug := range []bool{false, true} {
		t.Run(fmt.Sprintf("debug=%t", debug), func(t *testing.T) {
			output, errorOutput := setupWriterBoundaryTest(t, "")
			common.DebugEnabled = debug
			SetupLogger()
			firstWriter, firstErrorWriter, firstLogWriter := gin.DefaultWriter, gin.DefaultErrorWriter, log.Writer()
			SetupLogger()
			if gin.DefaultWriter != firstWriter || gin.DefaultErrorWriter != firstErrorWriter || log.Writer() != firstLogWriter {
				t.Error("repeated setup must not nest or replace existing guards")
			}
			panicErr := fmt.Errorf("upstream api_key=%s input_tokens=123 cost=0.125 request_id=req-recovery-17", "fictional-panic-credential")
			router := gin.New()
			router.Use(gin.Recovery())
			router.GET("/panic", func(*gin.Context) { panic(panicErr) })
			request := httptest.NewRequest(http.MethodGet, "/panic", nil)
			request.Header.Set("X-Api-Key", "fictional-header-credential")
			request.Header.Set("Cookie", "session=fictional-cookie-credential")
			request.Header.Set("X-Request-ID", "req-header-19")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("recovery response status = %d", response.Code)
			}
			mjError := struct {
				Code        int
				Description string
				Properties  any
				Result      string
			}{4, "api_key=fictional-mj-credential input_tokens=123 cost=0.125", map[string]any{"api_key": "fictional-mj-properties-credential", "audit_id": "audit-mj-37"}, "req-mj-31"}
			log.Printf("midjourney error: %+v", mjError)
			_, _ = fmt.Fprintln(gin.DefaultWriter, "api_key=fictional-gin-writer-credential request_id=req-output-41")
			got := output.String() + errorOutput.String()
			for _, secret := range []string{"fictional-panic-credential", "fictional-header-credential", "fictional-cookie-credential", "fictional-mj-credential", "fictional-mj-properties-credential", "fictional-gin-writer-credential"} {
				if strings.Contains(got, secret) {
					t.Errorf("writer boundary leaked %s", secret)
				}
			}
			for _, safe := range []string{"input_tokens=123", "cost=0.125", "req-recovery-17", "req-header-19", "req-mj-31", "audit-mj-37", "req-output-41"} {
				if !strings.Contains(got, safe) {
					t.Errorf("writer boundary lost %s", safe)
				}
			}
		})
	}
}

func TestSetupLoggerProtectsFileWritersAndStandardLogAfterRotation(t *testing.T) {
	setupWriterBoundaryTest(t, t.TempDir())
	SetupLogger()
	_, _ = fmt.Fprintln(gin.DefaultWriter, "api_key=fictional-file-out-credential request_id=req-file-out-17")
	_, _ = fmt.Fprintln(gin.DefaultErrorWriter, "api_key=fictional-file-error-credential request_id=req-file-error-19")
	log.Print("api_key=fictional-file-std-credential input_tokens=123 cost=0.125 request_id=req-file-std-31")
	SetupLogger()
	log.Print("api_key=fictional-file-rotated-credential request_id=req-file-rotated-41")
	data, err := os.ReadFile(GetCurrentLogPath())
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, "fictional-file-") {
		t.Error("file sink leaked a credential")
	}
	for _, safe := range []string{"input_tokens=123", "cost=0.125", "req-file-out-17", "req-file-error-19", "req-file-std-31", "req-file-rotated-41"} {
		if !strings.Contains(got, safe) {
			t.Errorf("file sink lost %s", safe)
		}
	}
}

func TestRuntimeLogRedactsEmbeddedStructuredCredentials(t *testing.T) {
	for _, debug := range []bool{false, true} {
		t.Run(fmt.Sprintf("debug=%t", debug), func(t *testing.T) {
			output, errorOutput := setupWriterBoundaryTest(t, "")
			common.DebugEnabled = debug
			SetupLogger()
			message := `body={"api_key":{"a":"FICTIONAL_EMBEDDED_A","b":"FICTIONAL_EMBEDDED_B"},"input_tokens":1234567890123456789,"cost":0.0373,"request_id":"req-17","idempotency_key":"charge-19"} env=[{"UNKNOWN":"FICTIONAL_EMBEDDED_C"}]`
			LogInfo(context.Background(), message)
			LogDebug(context.Background(), "%s", message)
			log.Printf("midjourney response: %s", message)
			got := output.String() + errorOutput.String()
			if strings.Contains(got, "FICTIONAL_EMBEDDED_") {
				t.Fatal("runtime sink leaked an embedded structured credential")
			}
			for _, safe := range []string{"1234567890123456789", "0.0373", "req-17", "charge-19"} {
				if !strings.Contains(got, safe) {
					t.Errorf("runtime sink lost %s", safe)
				}
			}
		})
	}
}

func TestRuntimeLogSinksRedactSecretsAndRequestContext(t *testing.T) {
	for _, debug := range []bool{false, true} {
		t.Run(fmt.Sprintf("debug=%t", debug), func(t *testing.T) {
			oldDebug := common.DebugEnabled
			common.DebugEnabled = debug
			t.Cleanup(func() { common.DebugEnabled = oldDebug })
			var output bytes.Buffer
			common.LogWriterMu.Lock()
			oldWriter, oldErrorWriter := gin.DefaultWriter, gin.DefaultErrorWriter
			gin.DefaultWriter, gin.DefaultErrorWriter = &output, &output
			common.LogWriterMu.Unlock()
			t.Cleanup(func() {
				common.LogWriterMu.Lock()
				gin.DefaultWriter, gin.DefaultErrorWriter = oldWriter, oldErrorWriter
				common.LogWriterMu.Unlock()
			})
			for _, sink := range []struct {
				name string
				log  func(context.Context, string)
			}{
				{"info", LogInfo}, {"warn", LogWarn}, {"error", LogError},
				{"debug", func(ctx context.Context, msg string) { LogDebug(ctx, "%s", msg) }},
			} {
				t.Run(sink.name, func(t *testing.T) {
					output.Reset()
					ctx := context.WithValue(context.Background(), common.RequestIdKey, "Authorization: Bearer fictional-request-credential")
					sink.log(ctx, `{"api_key":"fictional-json-credential"} Authorization: Bearer fictional-header-credential`+"\nOPENAI_API_KEY=fictional-env-credential\n"+`UPDATE channels SET api_key='fictional-sql-credential'; prompt_tokens=123 completion_tokens=45 cost=0.125 request_id=req-audit-17`)
					got := output.String()
					if sink.name == "debug" && !debug {
						if got != "" {
							t.Fatal("disabled debug logger emitted output")
						}
						return
					}
					for _, secret := range []string{"fictional-request-credential", "fictional-json-credential", "fictional-header-credential", "fictional-env-credential", "fictional-sql-credential"} {
						if strings.Contains(got, secret) {
							t.Errorf("%s leaked %s", sink.name, secret)
						}
					}
					for _, safe := range []string{"prompt_tokens=123", "completion_tokens=45", "cost=0.125", "req-audit-17"} {
						if !strings.Contains(got, safe) {
							t.Errorf("%s lost %s", sink.name, safe)
						}
					}
				})
			}
		})
	}
}
