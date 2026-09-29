package middleware

import (
	"net"
	"net/http"
	"strconv"
	"strings"
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

// RateLimitByUser caps requests per signed-in user (falling back to the IP),
// answering with the RATE_LIMITED code the game clients understand. It is a
// guard against scripted play loops rather than against shared networks, so
// it keys on the account first and never blocks a whole IP for one user.
func RateLimitByUser(limit int, window time.Duration) func(http.Handler) http.Handler {
	var (
		mu        sync.Mutex
		buckets   = map[string]*rateLimitBucket{}
		lastSweep = time.Now()
	)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, _ := r.Context().Value(UserIDContextKey).(string)
			if key == "" {
				key = "ip:" + clientIP(r)
			}
			now := time.Now()

			mu.Lock()
			if now.Sub(lastSweep) > window {
				for k, b := range buckets {
					if now.Sub(b.windowStart) > window {
						delete(buckets, k)
					}
				}
				lastSweep = now
			}
			bucket, ok := buckets[key]
			if !ok || now.Sub(bucket.windowStart) > window {
				bucket = &rateLimitBucket{windowStart: now}
				buckets[key] = bucket
			}
			bucket.count++
			blocked := bucket.count > limit
			retry := window - now.Sub(bucket.windowStart)
			mu.Unlock()

			if blocked {
				w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
				utils.JSON(w, http.StatusTooManyRequests, map[string]any{
					"error": "too many plays, please slow down",
					"code":  "RATE_LIMITED",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimitByDevice caps requests per device, from the X-Device-Id header
// the app sends (spec §7: per user/device/IP). Requests without one pass
// through; the per-user and per-IP limits still apply to them.
func RateLimitByDevice(limit int, window time.Duration) func(http.Handler) http.Handler {
	return rateLimitByKey(limit, window, func(r *http.Request) string {
		if id := DeviceID(r); id != "" {
			return "device:" + id
		}
		return ""
	})
}

// RateLimitPlaysByIP caps requests per network address with the RATE_LIMITED
// code. The limit is set well above what one household's players reach, so
// a shared network is not blocked for one account's behaviour.
func RateLimitPlaysByIP(limit int, window time.Duration) func(http.Handler) http.Handler {
	return rateLimitByKey(limit, window, func(r *http.Request) string { return "ip:" + clientIP(r) })
}

// DeviceIDHeader identifies the device a play came from.
const DeviceIDHeader = "X-Device-Id"

// DeviceID is the request's device id when it is well formed, else "".
func DeviceID(r *http.Request) string {
	id := strings.TrimSpace(r.Header.Get(DeviceIDHeader))
	if len(id) < 8 || len(id) > 64 {
		return ""
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return ""
		}
	}
	return id
}

func rateLimitByKey(limit int, window time.Duration, key func(*http.Request) string) func(http.Handler) http.Handler {
	var (
		mu        sync.Mutex
		buckets   = map[string]*rateLimitBucket{}
		lastSweep = time.Now()
	)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			k := key(r)
			if k == "" {
				next.ServeHTTP(w, r)
				return
			}
			now := time.Now()
			mu.Lock()
			if now.Sub(lastSweep) > window {
				for bk, b := range buckets {
					if now.Sub(b.windowStart) > window {
						delete(buckets, bk)
					}
				}
				lastSweep = now
			}
			bucket, ok := buckets[k]
			if !ok || now.Sub(bucket.windowStart) > window {
				bucket = &rateLimitBucket{windowStart: now}
				buckets[k] = bucket
			}
			bucket.count++
			blocked := bucket.count > limit
			retry := window - now.Sub(bucket.windowStart)
			mu.Unlock()
			if blocked {
				w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
				utils.JSON(w, http.StatusTooManyRequests, map[string]any{
					"error": "too many plays, please slow down",
					"code":  "RATE_LIMITED",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
