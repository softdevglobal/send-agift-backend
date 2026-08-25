package middleware

import (
	"net"
	"net/http"
	"sync"
	"time"

	"myapp/internal/utils"
)

// rateLimitBucket tracks one client's request count inside the current window.
type rateLimitBucket struct {
	count       int
	windowStart time.Time
}

// RateLimitByIP caps how many requests a single IP may make per window.
// It guards endpoints that cost money downstream (the Google Places proxy)
// and are reachable without a token.
func RateLimitByIP(limit int, window time.Duration) func(http.Handler) http.Handler {
	var (
		mu      sync.Mutex
		buckets = map[string]*rateLimitBucket{}
		// lastSweep is when we last dropped expired buckets, so the map does
		// not grow forever on a long-running process.
		lastSweep = time.Now()
	)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			now := time.Now()

			mu.Lock()
			if now.Sub(lastSweep) > window {
				for key, b := range buckets {
					if now.Sub(b.windowStart) > window {
						delete(buckets, key)
					}
				}
				lastSweep = now
			}

			bucket, ok := buckets[ip]
			if !ok || now.Sub(bucket.windowStart) > window {
				bucket = &rateLimitBucket{windowStart: now}
				buckets[ip] = bucket
			}
			bucket.count++
			blocked := bucket.count > limit
			mu.Unlock()

			if blocked {
				utils.Error(w, http.StatusTooManyRequests, "too many requests, please slow down")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP prefers the address chi's RealIP middleware resolved.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
