package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func copyDeliveryResult(c *gin.Context, src *http.Response, data []byte) error {
	return IOCopyBytesGracefully(c, src, data)
}

type copyDeliveryFailureWriter struct {
	*httptest.ResponseRecorder
	short    bool
	writeErr error
	flushErr error
	writes   int
	flushes  int
	cancel   context.CancelFunc
}

func (w *copyDeliveryFailureWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	if w.short {
		return w.ResponseRecorder.Write(data[:len(data)/2])
	}
	n, err := w.ResponseRecorder.Write(data)
	if w.cancel != nil {
		w.cancel()
	}
	return n, err
}

func (w *copyDeliveryFailureWriter) FlushError() error {
	w.flushes++
	return w.flushErr
}

func (w *copyDeliveryFailureWriter) Flush() {
	w.flushes++
	w.ResponseRecorder.Flush()
}

// A writer with only Flush, like middleware that cannot expose FlushError.
type copyDeliveryPanicWriter struct{ http.ResponseWriter }

func (w copyDeliveryPanicWriter) Flush() { panic("synthetic flush failure") }

func TestIOCopyBytesGracefullyDeliveryFailures(t *testing.T) {
	for _, tt := range []struct {
		name   string
		writer *copyDeliveryFailureWriter
		want   error
	}{
		{"short write", &copyDeliveryFailureWriter{short: true}, io.ErrShortWrite},
		{"write error", &copyDeliveryFailureWriter{writeErr: io.ErrClosedPipe}, io.ErrClosedPipe},
		{"flush error hidden by Gin", &copyDeliveryFailureWriter{flushErr: io.ErrClosedPipe}, io.ErrClosedPipe},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.writer.ResponseRecorder = httptest.NewRecorder()
			c, _ := gin.CreateTestContext(tt.writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			err := copyDeliveryResult(c, nil, bytes.Repeat([]byte("x"), 1<<20))
			require.ErrorIs(t, err, tt.want, "delivery failure must be observable by callers")
			require.Equal(t, 1, tt.writer.writes, "a partial response must not be replayed")
			if tt.name != "flush error hidden by Gin" {
				require.Zero(t, tt.writer.flushes, "do not flush again after a failed write")
			}
		})
	}
}

func TestIOCopyBytesGracefullyFlushPanicIsDeliveryError(t *testing.T) {
	c, _ := gin.CreateTestContext(copyDeliveryPanicWriter{httptest.NewRecorder()})
	require.NotPanics(t, func() {
		require.Error(t, copyDeliveryResult(c, nil, []byte("body")))
	})
}

func TestIOCopyBytesGracefullyHealthWriterPreservesFlushFailure(t *testing.T) {
	writer := &copyDeliveryFailureWriter{ResponseRecorder: httptest.NewRecorder(), flushErr: io.ErrClosedPipe}
	c, _ := gin.CreateTestContext(writer)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	c.Writer = relaycommon.NewZTAPIHealthResponseWriter(c.Writer, relaycommon.NewZTAPIHealthCollector("offline-test"))
	require.ErrorIs(t, copyDeliveryResult(c, nil, []byte("body")), io.ErrClosedPipe)
}

func TestIOCopyBytesGracefullyMissingWriterAndCanceledRequest(t *testing.T) {
	t.Run("missing writer", func(t *testing.T) {
		require.Error(t, copyDeliveryResult(&gin.Context{}, nil, []byte("body")))
	})
	t.Run("canceled before write", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil).WithContext(ctx)
		require.ErrorIs(t, copyDeliveryResult(c, nil, []byte("body")), context.Canceled)
		require.False(t, c.Writer.Written())
		require.Empty(t, recorder.Header())
	})
	t.Run("canceled during write", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		writer := &copyDeliveryFailureWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
		c, _ := gin.CreateTestContext(writer)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil).WithContext(ctx)
		require.ErrorIs(t, copyDeliveryResult(c, nil, []byte("body")), context.Canceled)
		require.Equal(t, "body", writer.Body.String())
		require.Zero(t, writer.flushes)
	})
}

func TestIOCopyBytesGracefullyLargeHTTPPayloads(t *testing.T) {
	pixels := image.NewGray(image.Rect(0, 0, 4096, 4096))
	state := uint32(1)
	for i := range pixels.Pix {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		pixels.Pix[i] = byte(state)
	}
	var pngBody bytes.Buffer
	require.NoError(t, (&png.Encoder{CompressionLevel: png.NoCompression}).Encode(&pngBody, pixels))
	jsonBody := []byte(`{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(pngBody.Bytes()) + `"}]}`)
	require.Greater(t, len(pngBody.Bytes()), 16<<20)
	var gzipBody bytes.Buffer
	gz, err := gzip.NewWriterLevel(&gzipBody, gzip.NoCompression)
	require.NoError(t, err)
	_, err = gz.Write(jsonBody)
	require.NoError(t, err)
	require.NoError(t, gz.Close())

	for _, tt := range []struct {
		name, contentType string
		wire, decoded     []byte
		gzip, autoDecode  bool
	}{
		{"4K PNG", "image/png", pngBody.Bytes(), pngBody.Bytes(), false, false},
		{"large JSON", "application/json", jsonBody, jsonBody, false, false},
		{"gzip preserved", "application/json", gzipBody.Bytes(), jsonBody, true, false},
		{"gzip automatically decoded", "application/json", gzipBody.Bytes(), jsonBody, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.Header().Set("Content-Length", strconv.Itoa(len(tt.wire)))
				if tt.gzip {
					w.Header().Set("Content-Encoding", "gzip")
				}
				_, _ = w.Write(tt.wire)
			}))
			defer upstream.Close()
			upstreamTransport := &http.Transport{DisableCompression: !tt.autoDecode}
			defer upstreamTransport.CloseIdleConnections()
			upstreamClient := &http.Client{Transport: upstreamTransport, Timeout: 10 * time.Second}
			copied := make(chan error, 1)
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				resp, readErr := upstreamClient.Get(upstream.URL)
				if readErr != nil {
					copied <- readErr
					return
				}
				defer resp.Body.Close()
				data, readErr := io.ReadAll(resp.Body)
				if readErr != nil {
					copied <- readErr
					return
				}
				// Copy must describe the bytes actually handed to it, not a stale length.
				resp.Header.Set("Content-Length", "7")
				resp.Header.Set("X-Provider-Debug", "synthetic-private-value")
				c, _ := gin.CreateTestContext(w)
				c.Request = r
				copied <- copyDeliveryResult(c, resp, data)
			}))
			defer proxy.Close()
			transport := &http.Transport{DisableCompression: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
			resp, err := client.Post(proxy.URL+"/v1/images/generations", "application/json", nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			wire, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Empty(t, resp.Header.Get("X-Provider-Debug"))
			require.EqualValues(t, len(wire), resp.ContentLength)
			if tt.gzip && !tt.autoDecode {
				require.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
				decoder, err := gzip.NewReader(bytes.NewReader(wire))
				require.NoError(t, err)
				decoded, err := io.ReadAll(decoder)
				require.NoError(t, err)
				require.NoError(t, decoder.Close())
				require.True(t, bytes.Equal(tt.decoded, decoded))
			} else {
				require.Empty(t, resp.Header.Get("Content-Encoding"))
				require.True(t, bytes.Equal(tt.decoded, wire))
			}
			require.NoError(t, <-copied)
			t.Logf("complete HTTP 200: wire=%d decoded=%d", len(wire), len(tt.decoded))
		})
	}
}

type copyDeliveryDisconnectWriter struct {
	http.ResponseWriter
	ctx context.Context
}

func (w copyDeliveryDisconnectWriter) Flush() {
	w.ResponseWriter.(http.Flusher).Flush()
}

func (w copyDeliveryDisconnectWriter) FlushError() error {
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w copyDeliveryDisconnectWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data[:1024])
	if err != nil {
		return n, err
	}
	if err := http.NewResponseController(w.ResponseWriter).Flush(); err != nil {
		return n, err
	}
	// The real client closes after receiving these headers. Do not race its
	// close against the kernel accepting the entire large body.
	select {
	case <-w.ctx.Done():
	case <-time.After(5 * time.Second):
		return n, errors.New("client disconnect synchronization timed out")
	}
	written, err := w.ResponseWriter.Write(data[n:])
	return n + written, err
}

func TestIOCopyBytesGracefullyRealHTTPDisconnect(t *testing.T) {
	data := bytes.Repeat([]byte("synthetic-image-data"), 2<<20)
	result := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := gin.CreateTestContext(copyDeliveryDisconnectWriter{w, r.Context()})
		c.Request = r
		result <- copyDeliveryResult(c, nil, data)
	}))
	defer server.Close()
	transport := &http.Transport{DisableCompression: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	resp, err := client.Get(server.URL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	select {
	case err := <-result:
		require.Error(t, err, "HTTP 200 headers do not mean the body was delivered")
	case <-time.After(5 * time.Second):
		t.Fatal("copy did not stop after the client disconnected")
	}
}

func TestIOCopyBytesGracefullyRealHTTPFlushFailure(t *testing.T) {
	result := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(-time.Second)); err != nil {
			result <- err
			return
		}
		c, _ := gin.CreateTestContext(w)
		c.Request = r
		result <- copyDeliveryResult(c, nil, []byte("buffered body"))
	}))
	defer server.Close()
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	resp, err := client.Get(server.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err, "the expired socket write deadline must reach the client")
	select {
	case err := <-result:
		require.Error(t, err, "buffered Write success must not hide a socket flush failure")
		var netErr interface{ Timeout() bool }
		require.True(t, errors.As(err, &netErr))
		require.True(t, netErr.Timeout())
	case <-time.After(5 * time.Second):
		t.Fatal("flush did not finish")
	}
}

// This deliberately inconsistent fixture is not an observation of a supplier.
// Opaque byte copying cannot prove that a declared content encoding is truthful.
func TestIOCopyBytesGracefullyHTTPEncodingMismatchIsNotWriteFailure(t *testing.T) {
	result := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := gin.CreateTestContext(w)
		c.Request = r
		result <- copyDeliveryResult(c, &http.Response{StatusCode: http.StatusOK, Header: http.Header{
			"Content-Type": {"application/json"}, "Content-Encoding": {"gzip"},
		}}, []byte(`{"data":[]}`))
	}))
	defer server.Close()
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	resp, err := client.Get(server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_, err = io.ReadAll(resp.Body)
	require.ErrorIs(t, err, gzip.ErrHeader)
	require.NoError(t, <-result, "a completed socket write is not proof of successful client decoding")
}
