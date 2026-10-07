package id

import (
	"testing"
	"time"
)

// RFC 4226 appendix D vectors: secret "12345678901234567890", 6 digits.
var hotpVectors = []string{
	"755224", "287082", "359152", "969429", "338314",
	"254676", "287922", "162583", "399871", "520489",
}

func TestHOTPVectors(t *testing.T) {
	key := []byte("12345678901234567890")
	for count, want := range hotpVectors {
		if got := hotp(key, uint64(count)); got != want {
			t.Fatalf("hotp(%d): want %s, got %s", count, want, got)
		}
	}
}

func TestTOTP(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))

	// T=59s → counter 1 → RFC 4226 vector for count 1
	code, err := totpCode(secret, time.Unix(59, 0))
	if err != nil {
		t.Fatal(err)
	}
	if code != "287082" {
		t.Fatalf("totp at t=59: want 287082, got %s", code)
	}

	now := time.Unix(59, 0)
	if step, ok := VerifyTOTP(secret, "287082", now); !ok || step != 1 {
		t.Fatalf("current code rejected (ok=%v step=%d)", ok, step)
	}
	// previous step (counter 0) accepted within skew window, reports step 0
	if step, ok := VerifyTOTP(secret, "755224", now); !ok || step != 0 {
		t.Fatalf("previous-step code rejected (ok=%v step=%d)", ok, step)
	}
	// two steps back is outside the window
	if _, ok := VerifyTOTP(secret, "755224", time.Unix(59+60, 0)); ok {
		t.Fatal("stale code accepted")
	}
	if _, ok := VerifyTOTP(secret, "000000", now); ok {
		t.Fatal("wrong code accepted")
	}
	if _, ok := VerifyTOTP(secret, "28708", now); ok {
		t.Fatal("short code accepted")
	}
	if _, ok := VerifyTOTP("!!!notbase32!!!", "287082", now); ok {
		t.Fatal("garbage secret accepted")
	}
}

func TestGenerateTOTPSecretAndURI(t *testing.T) {
	s := GenerateTOTPSecret()
	if len(s) != 32 { // 20 bytes → 32 base32 chars
		t.Fatalf("secret length: %d", len(s))
	}
	if _, err := totpCode(s, time.Now()); err != nil {
		t.Fatal(err)
	}
	uri := OtpauthURI("a@b.c", s)
	if want := "otpauth://totp/Agora:a@b.c?secret=" + s; len(uri) < len(want) {
		t.Fatalf("uri too short: %s", uri)
	}
}
