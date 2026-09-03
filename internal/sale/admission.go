package sale

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"agora/internal/authn"
	"agora/internal/httpx"
)

// ponytail: velocity limiting only. Device-fingerprint clustering and
// account-age scoring from spec 7.6 need client-side signal collection this
// pass doesn't have; add when a drop actually gets scalped.
const (
	velocityWindow = time.Minute
	velocityLimit  = 20 // reservation attempts per user+IP per window before shadow-queueing
	admissionBatch = 2.0 // overshoot factor: admit remaining*admissionBatch per tick
)

func velocityKey(userID, ip string) string {
	return fmt.Sprintf("sale:velocity:%s:%s", userID, ip)
}

// shadowQueued reports whether this caller is over the velocity threshold.
// Suspicious callers are still queued, just penalized (spec 7.6: shadow-queue,
// don't hard-block, so a scripted buyer can't learn the threshold).
func (s *Server) shadowQueued(ctx context.Context, userID, ip string) (bool, error) {
	n, err := s.rdb.rdb.Incr(ctx, velocityKey(userID, ip)).Result()
	if err != nil {
		return false, err
	}
	if n == 1 {
		s.rdb.rdb.Expire(ctx, velocityKey(userID, ip), velocityWindow)
	}
	return n > velocityLimit, nil
}

func (s *Server) handleJoinQueue(w http.ResponseWriter, r *http.Request) {
	dropID := r.PathValue("id")
	user := authn.UserID(r.Context())
	shadow, err := s.shadowQueued(r.Context(), user, clientIP(r))
	if err != nil {
		httpx.Error(w, 500, "queue")
		return
	}
	score := float64(time.Now().UnixNano())
	if shadow {
		score += float64(time.Hour.Nanoseconds()) // penalize: sinks to the back
	}
	if err := s.rdb.rdb.ZAdd(r.Context(), queueKey(dropID), redis.Z{Score: score, Member: user}).Err(); err != nil {
		httpx.Error(w, 500, "queue")
		return
	}
	httpx.JSON(w, 202, map[string]any{"queued": true})
}

type queueStatus struct {
	Admitted bool  `json:"admitted"`
	Position int64 `json:"position,omitempty"`
	Token    string `json:"token,omitempty"`
}

func (s *Server) queueStatus(ctx context.Context, dropID, user string) (queueStatus, error) {
	isAdmitted, err := s.rdb.rdb.SIsMember(ctx, admittedKey(dropID), user).Result()
	if err != nil {
		return queueStatus{}, err
	}
	if isAdmitted {
		return queueStatus{Admitted: true, Token: s.signQueueToken(dropID, user)}, nil
	}
	rank, err := s.rdb.rdb.ZRank(ctx, queueKey(dropID), user).Result()
	if err == redis.Nil {
		return queueStatus{Position: -1}, nil // not queued at all
	}
	if err != nil {
		return queueStatus{}, err
	}
	return queueStatus{Position: rank + 1}, nil
}

// handleQueueStream is a minimal SSE position feed: poll + push, no new deps.
func (s *Server) handleQueueStream(w http.ResponseWriter, r *http.Request) {
	dropID := r.PathValue("id")
	user := authn.UserID(r.Context())
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.Error(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		st, err := s.queueStatus(r.Context(), dropID, user)
		if err == nil {
			b, _ := json.Marshal(st)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
			if st.Admitted {
				return
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// signQueueToken carries drop id, user, and expiry (spec 7.2), HMAC-signed so
// the reserve handler can trust admission without a second Redis round trip.
func (s *Server) signQueueToken(dropID, user string) string {
	exp := time.Now().Add(10 * time.Minute).Unix()
	payload := fmt.Sprintf("%s:%s:%d", dropID, user, exp)
	mac := hmac.New(sha256.New, s.tokenSecret)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig
}

// AdmissionWorker promotes queued users into the admitted set in batches
// sized to remaining stock (spec 7.2), covering admits who never complete.
func (s *Server) AdmissionWorker(ctx context.Context, dropID string, shardCount int, every time.Duration) {
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				remaining, err := s.rdb.Remaining(ctx, dropID, shardCount)
				if err != nil || remaining <= 0 {
					continue
				}
				batch := int64(float64(remaining) * admissionBatch)
				if batch < 1 {
					batch = 1
				}
				users, err := s.rdb.rdb.ZPopMin(ctx, queueKey(dropID), batch).Result()
				if err != nil || len(users) == 0 {
					continue
				}
				ids := make([]any, len(users))
				for i, u := range users {
					ids[i] = u.Member
				}
				s.rdb.rdb.SAdd(ctx, admittedKey(dropID), ids...)
			}
		}
	}()
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return xff
	}
	return r.RemoteAddr
}
