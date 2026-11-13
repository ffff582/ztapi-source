package common

import (
	"context"
	"io"
	"sync"
	"time"
)

type ztapiReceiptDrainReader struct {
	client context.Context
	drain  *ZTAPIResponsesReceiptDrain
	input  io.Reader
	read   int
}

func (r *ztapiReceiptDrainReader) Read(p []byte) (int, error) {
	if r.client.Err() == nil {
		return r.input.Read(p)
	}
	left := ZTAPIResponsesReceiptBytes - r.read
	if r.drain.Remaining(r.client) <= 0 || left <= 0 {
		return 0, r.client.Err()
	}
	n, err := r.input.Read(p[:min(len(p), left)])
	r.read += n
	return n, err
}

func (d *ZTAPIResponsesReceiptDrain) LimitReader(client context.Context, input io.Reader) io.Reader {
	if d == nil {
		return input
	}
	return &ztapiReceiptDrainReader{client: client, drain: d, input: input}
}

const (
	ZTAPIResponsesReceiptWindow = 750 * time.Millisecond
	ZTAPIResponsesReceiptBytes  = 64 << 10
)

// ZTAPIResponsesReceiptDrain only extends an existing transport after text
// completion. The first observation of cancellation freezes eligibility.
type ZTAPIResponsesReceiptDrain struct {
	mu       sync.Mutex
	armed    bool
	cancelAt time.Time
}

func (d *ZTAPIResponsesReceiptDrain) SetArmed(armed bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancelAt.IsZero() {
		d.armed = armed
	}
}

func (d *ZTAPIResponsesReceiptDrain) Armed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.armed
}

func (d *ZTAPIResponsesReceiptDrain) Remaining(client context.Context) time.Duration {
	if d == nil || client.Err() != context.Canceled {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancelAt.IsZero() {
		d.cancelAt = time.Now()
	}
	if !d.armed {
		return 0
	}
	return time.Until(d.cancelAt.Add(ZTAPIResponsesReceiptWindow))
}

func (d *ZTAPIResponsesReceiptDrain) TransportContext(client context.Context) (context.Context, context.CancelFunc) {
	if d == nil {
		return context.WithCancel(client)
	}
	base := context.WithoutCancel(client)
	var ctx context.Context
	var cancel context.CancelFunc
	if deadline, ok := client.Deadline(); ok {
		ctx, cancel = context.WithDeadline(base, deadline)
	} else {
		ctx, cancel = context.WithCancel(base)
	}
	stop := context.AfterFunc(client, func() {
		if client.Err() == context.DeadlineExceeded {
			// The transport owns the same deadline. Let it report expiry rather
			// than racing that timer with a manual cancellation.
			return
		}
		if remaining := d.Remaining(client); remaining > 0 {
			timer := time.NewTimer(remaining)
			defer timer.Stop()
			select {
			case <-ctx.Done():
			case <-timer.C:
			}
		}
		cancel()
	})
	return ctx, func() {
		stop()
		cancel()
	}
}
