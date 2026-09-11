package id

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"agora/internal/httpx"
	"agora/internal/ratelimit"
)

func (s *Server) mfaRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /mfa", s.handleMFAStatus)
	mux.HandleFunc("POST /mfa/enroll", s.handleMFAEnroll)
	mux.HandleFunc("POST /mfa/activate", ratelimit.New(2, 10).Wrap(s.handleMFAActivate))
	mux.HandleFunc("POST /mfa/disable", ratelimit.New(2, 10).Wrap(s.handleMFADisable))
	mux.HandleFunc("POST /login/totp", ratelimit.New(2, 10).Wrap(s.handleLoginTOTP))
	mux.HandleFunc("POST /login/recovery", ratelimit.New(2, 10).Wrap(s.handleLoginRecovery))
}

func hashCode(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}

// encryptTOTPSecret/decryptTOTPSecret: unlike passwords and recovery codes,
// a TOTP secret must be recoverable (every login re-derives the code from
// it), so encryption, not hashing. AES-256-GCM keyed by SHA-256 of
// TOTP_ENCRYPTION_KEY, the same passphrase-to-key shape this repo already
// uses for its other shared secrets (see SALE_TOKEN_SECRET).
func (s *Server) encryptTOTPSecret(plaintext string) (string, error) {
	block, err := aes.NewCipher(s.totpKey[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plaintext), nil)), nil
}

func (s *Server) decryptTOTPSecret(encoded string) (string, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(s.totpKey[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return "", errors.New("totp ciphertext too short")
	}
	nonce, ct := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (s *Server) mfaEnabled(r *http.Request, userID string) bool {
	var confirmed bool
	s.pool.QueryRow(r.Context(),
		`SELECT confirmed FROM id.totp_secrets WHERE user_id = $1`, userID).Scan(&confirmed)
	return confirmed
}

func (s *Server) handleMFAStatus(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionUser(r)
	if !ok {
		httpx.Error(w, 401, "not signed in")
		return
	}
	httpx.JSON(w, 200, map[string]bool{"enabled": s.mfaEnabled(r, u.ID)})
}

func (s *Server) handleMFAEnroll(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionUser(r)
	if !ok {
		httpx.Error(w, 401, "not signed in")
		return
	}
	if s.mfaEnabled(r, u.ID) {
		httpx.Error(w, 409, "mfa already enabled")
		return
	}
	secret := GenerateTOTPSecret()
	encrypted, err := s.encryptTOTPSecret(secret)
	if err != nil {
		httpx.Error(w, 500, "encrypt")
		return
	}
	if _, err := s.pool.Exec(r.Context(),
		`INSERT INTO id.totp_secrets (user_id, secret, confirmed) VALUES ($1, $2, false)
		 ON CONFLICT (user_id) DO UPDATE SET secret = $2, confirmed = false, created_at = now()`,
		u.ID, encrypted); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	httpx.JSON(w, 200, map[string]string{
		"secret":      secret,
		"otpauth_uri": OtpauthURI(u.Email, secret),
	})
}

func (s *Server) handleMFAActivate(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionUser(r)
	if !ok {
		httpx.Error(w, 401, "not signed in")
		return
	}
	var in struct{ Code string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.Error(w, 400, "bad json")
		return
	}
	var encrypted string
	var confirmed bool
	if err := s.pool.QueryRow(r.Context(),
		`SELECT secret, confirmed FROM id.totp_secrets WHERE user_id = $1`, u.ID).
		Scan(&encrypted, &confirmed); err != nil || confirmed {
		httpx.Error(w, 409, "no pending enrollment")
		return
	}
	secret, err := s.decryptTOTPSecret(encrypted)
	if err != nil {
		httpx.Error(w, 500, "decrypt")
		return
	}
	if _, ok := VerifyTOTP(secret, in.Code, time.Now()); !ok {
		s.audit(r.Context(), u.ID, "mfa.activate.fail", nil)
		httpx.Error(w, 400, "wrong code")
		return
	}
	// activate + mint recovery codes in one tx
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(),
		`UPDATE id.totp_secrets SET confirmed = true WHERE user_id = $1`, u.ID); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if _, err := tx.Exec(r.Context(),
		`DELETE FROM id.recovery_codes WHERE user_id = $1`, u.ID); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	codes := make([]string, 8)
	for i := range codes {
		codes[i] = randToken(6) // 8 chars base64url
		if _, err := tx.Exec(r.Context(),
			`INSERT INTO id.recovery_codes (user_id, code_hash) VALUES ($1, $2)`,
			u.ID, hashCode(codes[i])); err != nil {
			httpx.Error(w, 500, "db")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	s.audit(r.Context(), u.ID, "mfa.enroll", nil)
	httpx.JSON(w, 200, map[string]any{"recovery_codes": codes})
}

func (s *Server) handleMFADisable(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionUser(r)
	if !ok {
		httpx.Error(w, 401, "not signed in")
		return
	}
	var in struct{ Code string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.Error(w, 400, "bad json")
		return
	}
	var encrypted string
	if err := s.pool.QueryRow(r.Context(),
		`SELECT secret FROM id.totp_secrets WHERE user_id = $1 AND confirmed`, u.ID).
		Scan(&encrypted); err != nil {
		httpx.Error(w, 409, "mfa not enabled")
		return
	}
	secret, err := s.decryptTOTPSecret(encrypted)
	if err != nil {
		httpx.Error(w, 500, "decrypt")
		return
	}
	if _, ok := VerifyTOTP(secret, in.Code, time.Now()); !ok {
		httpx.Error(w, 400, "wrong code")
		return
	}
	s.pool.Exec(r.Context(), `DELETE FROM id.totp_secrets WHERE user_id = $1`, u.ID)
	s.pool.Exec(r.Context(), `DELETE FROM id.recovery_codes WHERE user_id = $1`, u.ID)
	s.audit(r.Context(), u.ID, "mfa.disable", nil)
	w.WriteHeader(204)
}

// pendingUser resolves the half-authenticated session created by a correct
// password when MFA is enabled.
func (s *Server) pendingUser(r *http.Request) (sessionUser, string, bool) {
	var u sessionUser
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return u, "", false
	}
	err = s.pool.QueryRow(r.Context(),
		`SELECT u.id, u.email, u.handle FROM id.sessions s JOIN id.users u ON u.id = s.user_id
		 WHERE s.id = $1 AND s.expires_at > now() AND s.pending_mfa`, c.Value).
		Scan(&u.ID, &u.Email, &u.Handle)
	return u, c.Value, err == nil
}

func (s *Server) handleLoginTOTP(w http.ResponseWriter, r *http.Request) {
	u, sid, ok := s.pendingUser(r)
	if !ok {
		httpx.Error(w, 401, "no pending mfa login")
		return
	}
	var in struct{ Code string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.Error(w, 400, "bad json")
		return
	}
	var encrypted string
	if err := s.pool.QueryRow(r.Context(),
		`SELECT secret FROM id.totp_secrets WHERE user_id = $1 AND confirmed`, u.ID).
		Scan(&encrypted); err != nil {
		httpx.Error(w, 409, "mfa not enabled")
		return
	}
	secret, err := s.decryptTOTPSecret(encrypted)
	if err != nil {
		httpx.Error(w, 500, "decrypt")
		return
	}
	step, ok := VerifyTOTP(secret, in.Code, time.Now())
	if !ok {
		s.audit(r.Context(), u.ID, "mfa.fail", nil)
		httpx.Error(w, 401, "wrong code")
		return
	}
	// single-use: reject a code whose step was already consumed (RFC 6238 §5.2).
	// The guarded UPDATE also serializes concurrent submissions of the same code.
	tag, err := s.pool.Exec(r.Context(),
		`UPDATE id.totp_secrets SET last_used_step = $2 WHERE user_id = $1 AND last_used_step < $2`,
		u.ID, step)
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if tag.RowsAffected() != 1 {
		s.audit(r.Context(), u.ID, "mfa.replay", nil)
		httpx.Error(w, 401, "code already used")
		return
	}
	s.pool.Exec(r.Context(), `UPDATE id.sessions SET pending_mfa = false WHERE id = $1`, sid)
	s.audit(r.Context(), u.ID, "mfa.success", nil)
	httpx.JSON(w, 200, u)
}

func (s *Server) handleLoginRecovery(w http.ResponseWriter, r *http.Request) {
	u, sid, ok := s.pendingUser(r)
	if !ok {
		httpx.Error(w, 401, "no pending mfa login")
		return
	}
	var in struct{ Code string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.Error(w, 400, "bad json")
		return
	}
	// single-use: the guarded UPDATE burns the code atomically
	tag, err := s.pool.Exec(r.Context(),
		`UPDATE id.recovery_codes SET used_at = now()
		 WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL`,
		u.ID, hashCode(in.Code))
	if err != nil {
		httpx.Error(w, 500, "db")
		return
	}
	if tag.RowsAffected() != 1 {
		s.audit(r.Context(), u.ID, "mfa.recovery.fail", nil)
		httpx.Error(w, 401, "invalid recovery code")
		return
	}
	s.pool.Exec(r.Context(), `UPDATE id.sessions SET pending_mfa = false WHERE id = $1`, sid)
	s.audit(r.Context(), u.ID, "mfa.recovery.used", nil)
	httpx.JSON(w, 200, u)
}
