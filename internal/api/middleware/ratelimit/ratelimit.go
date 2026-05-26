// Simple rate limiter for /auth/* endpoints
// Holds the per-IP token-bucket middleware mounted in front of the /auth/* endpoints.
// Restricts attacker to exhaust CPU by spamming POST /auth/register and prevents credential-stuffing pipeline requests.
// Notes:
// * Per-IP buckets in process memory for simplicity.
// * IP source is remote addr from request.
// * Buckets are GC'd lazily on each allow call. Cutoff is the configured idleTTL; idle buckets that haven't been touched in that window are evicted.
package ratelimit

import (
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/vpramatarov/micro-blog/internal/api/httpx"
	"golang.org/x/time/rate"
)

type RateLimitConfig struct {
	// RPS is the refill rate in tokens per second. For 5req / 60s use rate.Limit(5.0/60.0).
	RPS rate.Limit

	// bucket capacity (>=1).
	Burst int

	// how long a bucket may sit unused before it is evicted from memory on the nex Allow call. Defauts to 10 min if zero or negative.
	IdleTTL time.Duration
}

type ipBucket struct {
	limiter *rate.Limiter
	lastUse time.Time
}

type perIpStore struct {
	cfg     RateLimitConfig
	mutext  sync.Mutex
	buckets map[string]*ipBucket
	lastGC  time.Time
}

// Per returns the rate.Limit corresponding to events per duration.
// Example: Per(5, time.Minute) == 5/60 events per second.
func Per(events int, per time.Duration) rate.Limit {
	if per <= 0 || events <= 0 {
		return rate.Limit(0)
	}

	return rate.Limit(float64(events) / float64(per.Seconds()))
}

func PerIP(cfg RateLimitConfig, log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}

	if cfg.Burst < 1 {
		cfg.Burst = 1
	}

	if cfg.IdleTTL <= 0 {
		cfg.IdleTTL = 10 * time.Minute
	}

	store := newPerIpStore(cfg)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := clientIP(r)
			if !store.allow(key, time.Now()) {
				w.Header().Set("Retry-After", "60")
				log.Info("rate_limited", "ip", key, "path", r.URL.Path)
				httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests; try again later")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request) string {
	if ip, _, err := net.SplitHostPort(r.RemoteAddr); err != nil {
		return ip
	}

	return r.RemoteAddr
}

func newPerIpStore(cfg RateLimitConfig) *perIpStore {
	return &perIpStore{cfg: cfg, buckets: map[string]*ipBucket{}, lastGC: time.Now()}
}

func (s *perIpStore) allow(key string, now time.Time) bool {
	s.mutext.Lock()
	defer s.mutext.Unlock()
	s.mayBeGC(now)
	bucket, ok := s.buckets[key]
	if !ok {
		bucket = &ipBucket{limiter: rate.NewLimiter(s.cfg.RPS, s.cfg.Burst)}
		s.buckets[key] = bucket
	}

	bucket.lastUse = now
	return bucket.limiter.AllowN(now, 1)
}

// evicts buckets idle for longer than cfg.IdleTTL. Runs at most once per IdleTTL window.
func (s *perIpStore) mayBeGC(now time.Time) {
	if now.Sub(s.lastGC) < s.cfg.IdleTTL {
		return
	}

	cutoff := now.Add(-s.cfg.IdleTTL)
	for k, bucket := range s.buckets {
		if bucket.lastUse.Before(cutoff) {
			delete(s.buckets, k)
		}
	}

	s.lastGC = now
}
