package channel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	base "github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
)

type ztapiAdmissionPolicy struct {
	wait, lease, heartbeat, poll, waiterTTL time.Duration
	capacity                                int
}

var ztapiSolAdmissionPolicy = ztapiAdmissionPolicy{
	wait: 90 * time.Second, lease: 60 * time.Second, heartbeat: 10 * time.Second,
	poll: 100 * time.Millisecond, waiterTTL: 10 * time.Second, capacity: 16,
}

// A lease covers the response body, not just receipt of HTTP headers. Redis
// coordinates rolling-deploy instances sharing the same effective credential.
// Admission is before the durable dispatch marker; no dispatched call is replayed.
var ztapiAdmissionAcquire = redis.NewScript(`
local clock = redis.call('TIME')
local now = tonumber(clock[1])*1000 + math.floor(tonumber(clock[2])/1000)
local expired = redis.call('ZRANGEBYSCORE', KEYS[3], '-inf', now)
for _, id in ipairs(expired) do
  redis.call('ZREM', KEYS[2], id)
  redis.call('ZREM', KEYS[3], id)
end
if not redis.call('ZSCORE', KEYS[2], ARGV[1]) then
  if redis.call('ZCARD', KEYS[2]) >= tonumber(ARGV[2]) then return -1 end
  local sequence = redis.call('INCR', KEYS[4])
  redis.call('ZADD', KEYS[2], sequence, ARGV[1])
end
redis.call('ZADD', KEYS[3], now+tonumber(ARGV[3]), ARGV[1])
for i=2,4 do redis.call('PEXPIRE', KEYS[i], ARGV[5]) end
local first = redis.call('ZRANGE', KEYS[2], 0, 0)
if first[1] == ARGV[1] and redis.call('SET', KEYS[1], ARGV[1], 'NX', 'PX', ARGV[4]) then
  redis.call('ZREM', KEYS[2], ARGV[1])
  redis.call('ZREM', KEYS[3], ARGV[1])
  return 1
end
return 0
`)

var ztapiAdmissionRenew = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`)

var ztapiAdmissionRelease = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then redis.call('DEL', KEYS[1]) end
redis.call('ZREM', KEYS[2], ARGV[1])
redis.call('ZREM', KEYS[3], ARGV[1])
return 1
`)

type ztapiUpstreamLease struct {
	ctx    context.Context
	cancel context.CancelFunc
	client *redis.Client
	keys   []string
	owner  string
	policy ztapiAdmissionPolicy
	once   sync.Once
	done   chan struct{}
}

func ztapiAdmissionKeys(req *http.Request, info *relaycommon.RelayInfo) []string {
	if info == nil || info.ZTAPIPublicationSnapshot == nil ||
		info.ZTAPIPublicationSnapshot.SourceModel != "gpt-6.1-sol" {
		return nil
	}
	material := strings.ToLower(req.URL.Scheme+"://"+req.URL.Host) + "\x00" +
		info.ZTAPIPublicationSnapshot.SourceModel + "\x00" + ztapiHealthCredentialMaterial(info, req.Header)
	digest := sha256.Sum256([]byte(material))
	prefix := "ztapi:upstream-admission:v1:{" + hex.EncodeToString(digest[:]) + "}"
	return []string{prefix + ":lease", prefix + ":queue", prefix + ":deadlines", prefix + ":sequence"}
}

func ztapiAdmissionError(reason string, status int) *types.NewAPIError {
	return types.NewErrorWithStatusCode(errors.New(reason), types.ErrorCodeRequestQueueUnavailable, status,
		types.ErrOptionWithLocalOrigin(), types.ErrOptionWithSkipRetry())
}

func acquireZTAPIUpstreamLease(ctx context.Context, req *http.Request, info *relaycommon.RelayInfo) (*ztapiUpstreamLease, error) {
	keys := ztapiAdmissionKeys(req, info)
	if len(keys) == 0 {
		return nil, nil
	}
	if !base.RedisEnabled || base.RDB == nil {
		return nil, ztapiAdmissionError("upstream admission store unavailable", http.StatusServiceUnavailable)
	}
	policy := ztapiSolAdmissionPolicy
	leaseCtx, cancel := info.ZTAPIResponsesReceiptDrain.TransportContext(ctx)
	lease := &ztapiUpstreamLease{ctx: leaseCtx, cancel: cancel, client: base.RDB,
		keys: keys, owner: uuid.NewString(), policy: policy, done: make(chan struct{})}
	waitCtx, stopWait := context.WithTimeout(ctx, policy.wait)
	defer stopWait()
	admitted := false
	defer func() {
		if !admitted {
			lease.release()
		}
	}()
	ticker := time.NewTicker(policy.poll)
	defer ticker.Stop()
	for {
		if err := waitCtx.Err(); err != nil {
			if ctx.Err() != nil {
				return nil, ztapiAdmissionError("request cancelled before dispatch", 499)
			}
			return nil, ztapiAdmissionError("upstream admission wait timed out before dispatch", http.StatusGatewayTimeout)
		}
		opCtx, stop := context.WithTimeout(waitCtx, 2*time.Second)
		result, err := ztapiAdmissionAcquire.Run(opCtx, lease.client, keys, lease.owner, policy.capacity,
			policy.waiterTTL.Milliseconds(), policy.lease.Milliseconds(), (policy.wait + policy.lease).Milliseconds()).Int()
		stop()
		if err != nil {
			if waitCtx.Err() != nil {
				continue
			}
			return nil, ztapiAdmissionError("upstream admission store unavailable", http.StatusServiceUnavailable)
		}
		if result < 0 {
			return nil, ztapiAdmissionError("upstream admission queue full before dispatch", http.StatusTooManyRequests)
		}
		if result == 1 {
			if ctx.Err() != nil {
				return nil, ztapiAdmissionError("request cancelled before dispatch", 499)
			}
			admitted = true
			go lease.renew()
			return lease, nil
		}
		select {
		case <-waitCtx.Done():
		case <-ticker.C:
		}
	}
}

func (lease *ztapiUpstreamLease) renew() {
	defer close(lease.done)
	ticker := time.NewTicker(lease.policy.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-lease.ctx.Done():
			return
		case <-ticker.C:
			opCtx, stop := context.WithTimeout(lease.ctx, 2*time.Second)
			owned, err := ztapiAdmissionRenew.Run(opCtx, lease.client, lease.keys[:1], lease.owner, lease.policy.lease.Milliseconds()).Int()
			stop()
			if err != nil || owned != 1 {
				// Cancel only the outbound transport. The caller did not cancel.
				lease.cancel()
				return
			}
		}
	}
}

func (lease *ztapiUpstreamLease) release() {
	if lease == nil {
		return
	}
	lease.once.Do(func() {
		lease.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// Ownership check prevents an old instance releasing a replacement lease.
		_ = ztapiAdmissionRelease.Run(ctx, lease.client, lease.keys[:3], lease.owner).Err()
	})
}

type ztapiAdmittedResponseBody struct {
	io.ReadCloser
	lease     *ztapiUpstreamLease
	closeOnce sync.Once
	closeErr  error
}

func (body *ztapiAdmittedResponseBody) Close() error {
	body.closeOnce.Do(func() {
		body.closeErr = body.ReadCloser.Close()
		body.lease.release()
	})
	return body.closeErr
}
