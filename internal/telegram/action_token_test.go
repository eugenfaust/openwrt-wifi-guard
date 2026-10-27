package telegram

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestActionCodecRoundTripAndTelegramLimit(t *testing.T) {
	codec, err := NewActionCodec(bytes.Repeat([]byte{0x42}, 32), 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1790000000, 0).UTC()
	for _, action := range []Action{ActionTrust, ActionBlockPrompt, ActionBlockConfirm, ActionUnblockPrompt, ActionUnblockConfirm, ActionCancel} {
		claims := ActionClaims{Action: action, MAC: "02:00:00:00:00:01", Nonce: 42, Issued: now}
		token, err := codec.Encode(claims, 12345)
		if err != nil {
			t.Fatal(err)
		}
		if len(token) > 64 || !strings.HasPrefix(token, "wg1:") {
			t.Fatal("invalid Telegram callback format")
		}
		decoded, err := codec.Decode(token, 12345, now.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if decoded.Action != claims.Action || decoded.MAC != claims.MAC || decoded.Nonce != claims.Nonce || !decoded.Issued.Equal(claims.Issued) {
			t.Fatal("claims changed")
		}
	}
}
func TestActionCodecRejectsTamperReplayAcrossChatAndExpiry(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	codec, err := NewActionCodec(key, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	key[0] = 0 // Constructor must copy its key, not retain caller-owned mutable data.
	now := time.Unix(1790000000, 0).UTC()
	token, err := codec.Encode(ActionClaims{Action: ActionTrust, MAC: "02:00:00:00:00:01", Nonce: 42, Issued: now}, 12345)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := NewActionCodec(bytes.Repeat([]byte{0x43}, 32), time.Hour)
	if _, err := other.Decode(token, 12345, now); err == nil {
		t.Fatal("wrong key accepted")
	}
	if _, err := codec.Decode(token, 54321, now); err == nil {
		t.Fatal("cross-chat replay accepted")
	}
	if _, err := codec.Decode(token, 12345, now.Add(time.Hour+time.Second)); err == nil {
		t.Fatal("expired action accepted")
	}
	if _, err := codec.Decode(token, 12345, now.Add(-3*time.Minute)); err == nil {
		t.Fatal("future action accepted")
	}
	for n := 4; n < len(token); n++ {
		changed := []byte(token)
		if changed[n] == 'A' {
			changed[n] = 'B'
		} else {
			changed[n] = 'A'
		}
		if _, err := codec.Decode(string(changed), 12345, now); err == nil {
			t.Fatalf("tamper accepted at %d", n)
		}
	}
	for _, invalid := range []string{"", token + "=", token + "\n", "wg2:" + token[4:], "wg1:" + strings.Repeat("A", 100)} {
		if _, err := codec.Decode(invalid, 12345, now); err == nil {
			t.Fatal("invalid wire encoding accepted")
		}
	}
	fresh, _ := NewActionCodec(bytes.Repeat([]byte{0x42}, 32), time.Hour)
	if _, err := fresh.Decode(token, 12345, now); err != nil {
		t.Fatal("key mutation affected codec", err)
	}
}
func TestActionCodecRejectsInvalidClaimsAndConfiguration(t *testing.T) {
	for _, size := range []int{0, 16, 31, 33} {
		if _, err := NewActionCodec(make([]byte, size), time.Hour); err == nil {
			t.Fatal("bad key length accepted")
		}
	}
	if _, err := NewActionCodec(make([]byte, 32), 0); err == nil {
		t.Fatal("unbounded action lifetime")
	}
	codec, _ := NewActionCodec(bytes.Repeat([]byte{0x42}, 32), time.Hour)
	valid := ActionClaims{Action: ActionTrust, MAC: "02:00:00:00:00:01", Nonce: 42, Issued: time.Unix(1790000000, 0)}
	for _, change := range []func(*ActionClaims){func(c *ActionClaims) { c.Action = 255 }, func(c *ActionClaims) { c.MAC = "01:00:00:00:00:01" }, func(c *ActionClaims) { c.Nonce = 0 }, func(c *ActionClaims) { c.Issued = time.Unix(-1, 0) }, func(c *ActionClaims) { c.Issued = time.Unix(1<<32, 0) }} {
		invalid := valid
		change(&invalid)
		if _, err := codec.Encode(invalid, 12345); err == nil {
			t.Fatal("invalid claims accepted")
		}
	}
	if _, err := codec.Encode(valid, 0); err == nil {
		t.Fatal("missing chat binding")
	}
}
