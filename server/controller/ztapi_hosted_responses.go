package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/hostedsearch"
	"github.com/gin-gonic/gin"
)

type hostedCredentialContextKey struct{}
type hostedChildContextKey struct{}

func IsHostedSearchChild(request *http.Request) bool {
	child, _ := request.Context().Value(hostedChildContextKey{}).(bool)
	return child
}

// Authentication scrubs request credentials. Keep a request-scoped copy solely
// for the authenticated, in-process child relay; never put it in Gin/log keys.
func CaptureHostedResponsesCredential() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodPost && c.Request.URL.Path == "/v1/responses" {
			values := append([]string(nil), c.Request.Header.Values("Authorization")...)
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), hostedCredentialContextKey{}, values))
		}
		c.Next()
	}
}

type HostedResponsesOptions struct {
	Provider  hostedsearch.Provider
	Authorize func(*gin.Context, string) bool
	Preflight func(*gin.Context) error
	Dispatch  func(*gin.Context, string) hostedsearch.ToolDispatch
	Pages     func(*gin.Context) hostedsearch.PageStore
	// Acceptance instrumentation wraps every real model round, including hidden
	// continuations. This is never derived from client input or production env.
	ModelDispatch func(context.Context, []byte, hostedsearch.ModelRound) ([]byte, error)
}

func HostedResponsesBridge(router http.Handler, options HostedResponsesOptions) gin.HandlerFunc {
	// Bound orchestration, independent of ordinary model request concurrency.
	concurrency := make(chan struct{}, 8)
	// A per-request random declaration invalidates the upstream tools/system
	// prompt cache. Keep this name stable for the bridge process; external
	// declarations/history with its reserved prefix are rejected separately.
	privateName := hostedsearch.NewID("ztapi_hosted_web_")
	return func(c *gin.Context) {
		if IsHostedSearchChild(c.Request) {
			c.Next()
			return
		}
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			hostedHTTPError(c, http.StatusBadRequest, "hosted_search_invalid_request")
			return
		}
		c.Request.Body = io.NopCloser(storage)
		if storage.Size() > hostedsearch.MaxRequestBytes {
			c.Next()
			return
		}
		raw, err := storage.Bytes()
		if err != nil {
			hostedHTTPError(c, http.StatusBadRequest, "hosted_search_invalid_request")
			return
		}
		// Do not change native OpenAI or non-hosted request behavior.
		var hint struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(raw, &hint) != nil || !strings.HasPrefix(hint.Model, "zt-claude-") || (!bytes.Contains(raw, []byte(`"web_search"`)) && !bytes.Contains(raw, []byte(`"web_search_preview"`))) {
			c.Next()
			return
		}
		prepared, err := hostedsearch.PrepareRequest(raw, privateName)
		if err != nil {
			hostedHTTPError(c, http.StatusBadRequest, "hosted_search_invalid_request")
			return
		}
		if !prepared.Hosted {
			c.Next()
			return
		}
		c.Abort()
		if options.Authorize == nil || !options.Authorize(c, prepared.Model) {
			hostedHTTPError(c, http.StatusForbidden, "model_not_available")
			return
		}
		if router == nil || options.Provider == nil || options.Dispatch == nil {
			hostedHTTPError(c, http.StatusServiceUnavailable, "hosted_search_unavailable")
			return
		}
		if options.Preflight != nil && options.Preflight(c) != nil {
			hostedHTTPError(c, http.StatusServiceUnavailable, "hosted_search_billing_unavailable")
			return
		}
		credentials, _ := c.Request.Context().Value(hostedCredentialContextKey{}).([]string)
		if len(credentials) != 1 || credentials[0] == "" {
			hostedHTTPError(c, http.StatusUnauthorized, "hosted_search_authentication_required")
			return
		}
		select {
		case concurrency <- struct{}{}:
		default:
			hostedHTTPError(c, http.StatusTooManyRequests, "hosted_search_busy")
			return
		}
		workerOwnsLease := false
		defer func() {
			if !workerOwnsLease {
				<-concurrency
			}
		}()
		rootID := hostedsearch.NewID("hsroot_")
		c.Set(common.RequestIdKey, rootID)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), common.RequestIdKey, rootID))
		c.Header("X-Request-ID", rootID)
		c.Header(common.RequestIdKey, rootID)
		internalAcceptance := ztapiTrustedContextBool(c, constant.ContextKeyZTAPIAcceptanceSingleRound)
		var dispatch hostedsearch.ToolDispatch
		if internalAcceptance {
			// Greeting acceptance must not silently buy tools or another model round.
			dispatch = func(context.Context, hostedsearch.ToolRequest, func(context.Context) (hostedsearch.Result, error)) (hostedsearch.Result, error) {
				return hostedsearch.Result{}, hostedsearch.ErrLimit
			}
		} else {
			dispatch = options.Dispatch(c, rootID)
		}
		if dispatch == nil {
			hostedHTTPError(c, http.StatusServiceUnavailable, "hosted_search_billing_unavailable")
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Minute)
		defer cancel()
		clientIP := c.ClientIP()
		if net.ParseIP(clientIP) == nil {
			hostedHTTPError(c, http.StatusForbidden, "invalid_client_ip")
			return
		}
		round := 0
		config := hostedsearch.Config{Provider: options.Provider, Dispatch: dispatch,
			Model: func(ctx context.Context, body []byte) ([]byte, error) {
				round++
				if internalAcceptance && round > 1 {
					return nil, hostedsearch.ErrLimit
				}
				childContext := context.WithValue(ctx, hostedChildContextKey{}, true)
				request, err := http.NewRequestWithContext(childContext, http.MethodPost, "/v1/responses", bytes.NewReader(body))
				if err != nil {
					return nil, err
				}
				request.Header.Set("Authorization", credentials[0])
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("User-Agent", "ZTAPI-hosted-search")
				request.Header.Set("X-Request-ID", fmt.Sprintf("hs_%s_%d", rootID, round))
				request.RemoteAddr = net.JoinHostPort(clientIP, "0")
				response := &hostedChildResponse{header: make(http.Header)}
				router.ServeHTTP(response, request)
				if response.exceeded {
					return nil, hostedsearch.ErrLimit
				}
				if response.status != http.StatusOK {
					return nil, errors.New("hosted_search_model_round_failed")
				}
				return response.body.Bytes(), nil
			}}
		if options.ModelDispatch != nil {
			native := config.Model
			config.Model = func(ctx context.Context, body []byte) ([]byte, error) {
				return options.ModelDispatch(ctx, body, native)
			}
		}
		if options.Pages != nil {
			config.Pages = options.Pages(c)
		}
		c.Header("X-ZTAPI-Hosted-Root-ID", rootID)
		c.Header("X-ZTAPI-Tool-Pricing", "search=0.01CNY;fetch=0.01CNY;cached-find=0;model-tokens=separate")
		type completion struct {
			result *hostedsearch.RunResult
			err    error
		}
		done := make(chan completion, 1)
		events := make(chan hostedsearch.Event)
		var emit func(hostedsearch.Event) error
		if prepared.Stream {
			emit = func(event hostedsearch.Event) error {
				select {
				case events <- event:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		// A context-ignoring storage call must retain its bounded worker lease,
		// but must not prevent a disconnected client handler from returning.
		workerOwnsLease = true
		go func() {
			finished := completion{err: errors.New("hosted_search_worker_failed")}
			defer func() {
				if recover() != nil {
					finished = completion{err: errors.New("hosted_search_worker_failed")}
				}
				<-concurrency
				done <- finished
			}()
			finished.result, finished.err = hostedsearch.Run(ctx, prepared, config, emit)
		}()
		if !prepared.Stream {
			select {
			case finished := <-done:
				if finished.err != nil || finished.result == nil {
					hostedHTTPError(c, http.StatusBadGateway, "hosted_search_failed")
					return
				}
				c.Data(http.StatusOK, "application/json", finished.result.Body)
			case <-ctx.Done():
				hostedHTTPError(c, http.StatusGatewayTimeout, "hosted_search_canceled")
			}
			return
		}
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache, no-store")
		c.Header("X-Accel-Buffering", "no")
		heartbeat := time.NewTicker(10 * time.Second)
		defer heartbeat.Stop()
		for {
			select {
			case event := <-events:
				encoded, err := json.Marshal(event.Data)
				if err != nil {
					cancel()
					return
				}
				if _, err = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event.Type, encoded); err != nil {
					cancel()
					return
				}
				c.Writer.Flush()
			case <-heartbeat.C:
				if _, err := io.WriteString(c.Writer, ": keep-alive\n\n"); err != nil {
					cancel()
					return
				}
				c.Writer.Flush()
			case <-done:
				return
			case <-ctx.Done():
				cancel()
				return
			}
		}
	}
}

func hostedHTTPError(c *gin.Context, status int, code string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"type": "hosted_search_error", "code": code, "message": code}})
}

type hostedChildResponse struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	exceeded bool
}

func (w *hostedChildResponse) Header() http.Header { return w.header }
func (w *hostedChildResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *hostedChildResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.body.Len()+len(p) > 2*1024*1024 {
		w.exceeded = true
		return 0, hostedsearch.ErrLimit
	}
	return w.body.Write(p)
}
func (w *hostedChildResponse) Flush() {}
