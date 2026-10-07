// TOTP (RFC 6238) over HOTP (RFC 4226): HMAC-SHA1, 30-second steps, 6 digits.
// Hand-rolled like the JWTs, because the IdP's crypto is the curriculum.
package id

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"time"
)

const (
	totpStep   = 30 * time.Second
	totpDigits = 6
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func GenerateTOTPSecret() string {
	b := make([]byte, 20)
	rand.Read(b)
	return b32.EncodeToString(b)
}

func hotp(key []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", code%1_000_000)
}

func totpCode(secret string, t time.Time) (string, error) {
	key, err := b32.DecodeString(secret)
	if err != nil {
		return "", fmt.Errorf("bad totp secret")
	}
	return hotp(key, uint64(t.Unix())/uint64(totpStep.Seconds())), nil
}

// VerifyTOTP accepts the current step and one step either side (clock skew).
// On success it also returns the matched step counter so callers can enforce
// single-use (RFC 6238 §5.2) by refusing to accept the same step twice.
func VerifyTOTP(secret, code string, now time.Time) (int64, bool) {
	if len(code) != totpDigits {
		return 0, false
	}
	var step int64
	ok := false
	for _, dt := range []time.Duration{0, -totpStep, totpStep} {
		t := now.Add(dt)
		want, err := totpCode(secret, t)
		if err != nil {
			return 0, false
		}
		// constant-time compare each candidate; no early exit on match
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			step = int64(uint64(t.Unix()) / uint64(totpStep.Seconds()))
			ok = true
		}
	}
	return step, ok
}

// OtpauthURI renders the standard enrollment URI authenticator apps consume.
func OtpauthURI(email, secret string) string {
	return fmt.Sprintf("otpauth://totp/Agora:%s?secret=%s&issuer=Agora&algorithm=SHA1&digits=%d&period=%d",
		url.PathEscape(email), secret, totpDigits, int(totpStep.Seconds()))
}
