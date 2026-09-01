package openp2p

import "testing"

func TestRelayTOTPCompatibility(t *testing.T) {
	const token uint64 = 123456789
	const timestamp int64 = 1700000000
	const expected uint64 = 15383081134507884063
	if got := relayTOTP(token, timestamp); got != expected {
		t.Fatalf("relay TOTP = %d, want %d", got, expected)
	}
	if !relayTokenValid(expected, token, timestamp) || !relayTokenValid(token, token, timestamp) {
		t.Fatal("current TOTP and original token must be accepted")
	}
	if relayTokenValid(0, token, timestamp) || relayTokenValid(expected, token, timestamp+2*relayTOTPStep) {
		t.Fatal("zero and expired TOTP must be rejected")
	}
}
