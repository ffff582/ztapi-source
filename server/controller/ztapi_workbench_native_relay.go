package controller

import (
	"bytes"
	"errors"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// RelayZTAPIWorkbench is an opt-in entry for an authenticated, distributed
// Responses route. The existing public router does not register this handler.
func RelayZTAPIWorkbench(c *gin.Context) {
	if c.Request.Method != http.MethodPost || c.Request.URL.Path != "/v1/responses" || c.GetInt("id") < 1 || c.GetInt("token_id") < 1 ||
		len(c.Request.Header.Values("X-ZTAPI-Task")) != 1 || len(c.Request.Header.Values("X-ZTAPI-Hold")) != 1 {
		c.Status(http.StatusBadRequest)
		return
	}
	task, hold := c.GetHeader("X-ZTAPI-Task"), c.GetHeader("X-ZTAPI-Hold")
	if !workbenchReplayUUID.MatchString(task) || !workbenchReplayUUID.MatchString(hold) {
		c.Status(http.StatusBadRequest)
		return
	}
	service.BindZTAPIWorkbenchRelayHold(c, task, hold)
	outer := c.Writer
	buffer := &workbenchNativeResponse{ResponseWriter: outer, header: make(http.Header), status: http.StatusOK}
	c.Writer = buffer
	defer func() { c.Writer = outer }()
	Relay(c, types.RelayFormatOpenAIResponses)
	c.Writer = outer
	c.Header("Cache-Control", "no-store")
	if buffer.header.Get("X-ZTAPI-Workbench-Error") == "pilot-limit" {
		c.Header("X-ZTAPI-Workbench-Error", "pilot-limit")
		c.Status(http.StatusTooManyRequests)
		return
	}
	if buffer.status >= 400 && !buffer.overflow {
		c.Status(buffer.status)
		return
	}
	var row model.ZTAPIRequestSettlement
	if buffer.overflow || !strings.HasPrefix(buffer.header.Get("Content-Type"), "text/event-stream") ||
		model.DB == nil || model.DB.Where("operation_id = ? AND workbench_task_id = ? AND user_id = ? AND token_id = ?", hold, task, c.GetInt("id"), c.GetInt("token_id")).Take(&row).Error != nil ||
		row.Status != model.ZTAPISettlementSettled || !row.Dispatched || row.FinalAttempt != 1 || !bytes.Contains(buffer.body.Bytes(), []byte("event: response.completed\n")) {
		c.Status(http.StatusBadGateway)
		return
	}
	c.Header("X-ZTAPI-Budget-Hold", hold)
	c.Data(http.StatusOK, "text/event-stream", buffer.body.Bytes())
}

type workbenchNativeResponse struct {
	gin.ResponseWriter
	header   http.Header
	body     bytes.Buffer
	status   int
	written  bool
	overflow bool
}

func (w *workbenchNativeResponse) Header() http.Header { return w.header }
func (w *workbenchNativeResponse) Status() int         { return w.status }
func (w *workbenchNativeResponse) Written() bool       { return w.written }
func (w *workbenchNativeResponse) Size() int {
	if !w.written {
		return -1
	}
	return w.body.Len()
}
func (w *workbenchNativeResponse) WriteHeader(status int) {
	if !w.written && status > 0 {
		w.status = status
	}
}
func (w *workbenchNativeResponse) WriteHeaderNow() { w.written = true }
func (w *workbenchNativeResponse) Flush()          { w.WriteHeaderNow() }
func (w *workbenchNativeResponse) Write(p []byte) (int, error) {
	w.WriteHeaderNow()
	if w.overflow || len(p) > 1024*1024-w.body.Len() {
		w.overflow = true
		return 0, errors.New("workbench response exceeds buffer limit")
	}
	return w.body.Write(p)
}
func (w *workbenchNativeResponse) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
