package helper

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

type shutdownReader struct {
	*io.PipeReader
	reading chan struct{}
	once    sync.Once
}

func TestZTAPIStreamShutdownWaitsForSlowDrain(t *testing.T) {
	for _, ending := range []string{"data: [DONE]\n", ""} {
		t.Run(fmt.Sprintf("ending_%q", ending), func(t *testing.T) {
			c, resp, info := shutdownContext(io.NopCloser(strings.NewReader("data: first\n\ndata: last\n\n" + ending)))
			release := make(chan struct{})
			entered := make(chan struct{})
			done := make(chan struct{})
			count := 0
			go func() {
				StreamScannerHandler(c, resp, info, func(_ string, _ *StreamResult) {
					count++
					if count == 1 {
						close(entered)
						<-release
					}
				})
				close(done)
			}()
			<-entered
			select {
			case <-done:
				t.Error("returned before the client finished receiving queued frames")
			case <-time.After(5500 * time.Millisecond):
			}
			close(release)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("did not finish after writer resumed")
			}
			if count != 2 {
				t.Fatalf("got %d frames", count)
			}
		})
	}
}

func TestZTAPIStreamShutdownPingCannotStealHandlerStop(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	oldEnabled, oldInterval := settings.PingIntervalEnabled, settings.PingIntervalSeconds
	settings.PingIntervalEnabled, settings.PingIntervalSeconds = true, 60
	defer func() { settings.PingIntervalEnabled, settings.PingIntervalSeconds = oldEnabled, oldInterval }()
	for iteration := 0; iteration < 20; iteration++ {
		reader, writer := io.Pipe()
		c, resp, info := shutdownContext(reader)
		info.DisablePing = false
		ctx, cancel := context.WithCancel(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
		done := make(chan struct{})
		go func() {
			StreamScannerHandler(c, resp, info, func(_ string, sr *StreamResult) { sr.Done() })
			close(done)
		}()
		if _, err := writer.Write([]byte("data: completed\n\n")); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(300 * time.Millisecond):
			t.Errorf("iteration %d: ping consumed the handler stop notification", iteration)
			cancel()
			<-done
		}
		cancel()
		writer.Close()
	}
}

func (r *shutdownReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.reading) })
	return r.PipeReader.Read(p)
}

func shutdownContext(body io.ReadCloser) (*gin.Context, *http.Response, *relaycommon.RelayInfo) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c, &http.Response{Body: body}, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}, DisablePing: true}
}

func TestZTAPIStreamShutdownCancellationClosesBlockedRead(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	body := &shutdownReader{PipeReader: reader, reading: make(chan struct{})}
	c, resp, info := shutdownContext(body)
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	done := make(chan struct{})
	go func() {
		StreamScannerHandler(c, resp, info, func(string, *StreamResult) {})
		close(done)
	}()
	select {
	case <-body.reading:
	case <-time.After(time.Second):
		t.Fatal("scanner did not begin reading")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("cancellation waited for cleanup timeout instead of closing the upstream body")
		reader.Close()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Fatal("stream did not stop after upstream close")
		}
	}
	if info.StreamStatus.EndReason != relaycommon.StreamEndReasonClientGone {
		t.Fatalf("unexpected end reason: %s", info.StreamStatus.EndReason)
	}
}

func TestZTAPIStreamShutdownDrainsLongReplyBeforeReturn(t *testing.T) {
	for iteration := 0; iteration < 10; iteration++ {
		c, resp, info := shutdownContext(io.NopCloser(strings.NewReader(buildSSEBody(1000))))
		count := 0
		StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {
			if !strings.Contains(data, fmt.Sprintf("\"id\":%d,", count)) {
				sr.Stop(fmt.Errorf("out of order chunk %d", count))
				return
			}
			count++
			if count%100 == 0 {
				time.Sleep(time.Millisecond)
			}
		})
		if count != 1000 || info.StreamStatus.EndReason != relaycommon.StreamEndReasonDone {
			t.Fatalf("iteration %d: got %d chunks, reason %s", iteration, count, info.StreamStatus.EndReason)
		}
	}
}

func TestZTAPIStreamShutdownHandlerStopClosesBlockedRead(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	c, resp, info := shutdownContext(reader)
	done := make(chan struct{})
	go func() {
		StreamScannerHandler(c, resp, info, func(_ string, sr *StreamResult) { sr.Done() })
		close(done)
	}()
	if _, err := writer.Write([]byte("data: {\"tool\":\"complete\"}\n\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("handler completion left an upstream read blocked")
		reader.Close()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Fatal("stream did not stop")
		}
	}
}

func TestZTAPIStreamShutdownHandlerPanicWithFullBuffer(t *testing.T) {
	c, resp, info := shutdownContext(io.NopCloser(strings.NewReader(buildSSEBody(1000))))
	started := time.Now()
	StreamScannerHandler(c, resp, info, func(_ string, _ *StreamResult) {
		// Let the scanner fill its dispatch buffer before the callback fails.
		time.Sleep(20 * time.Millisecond)
		panic("synthetic handler failure")
	})
	if time.Since(started) > time.Second {
		t.Fatal("panic did not unblock scanner dispatch promptly")
	}
	if info.StreamStatus.EndReason != relaycommon.StreamEndReasonPanic {
		t.Fatalf("unexpected end reason: %s", info.StreamStatus.EndReason)
	}
}
