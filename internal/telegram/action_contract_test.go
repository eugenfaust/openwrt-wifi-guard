package telegram

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"testing"
	"time"
)

func TestActionCodecNonceAndLifetimeContracts(t *testing.T) {
	codec, _ := NewActionCodec(bytes.Repeat([]byte{42}, 32), 7*24*time.Hour)
	now := time.Unix(1790000000, 0).UTC()
	for _, nonce := range []uint64{1, 1 << 63, ^uint64(0)} {
		claims := ActionClaims{Action: ActionBlockConfirm, MAC: "02:00:00:00:00:01", Nonce: nonce, Issued: now}
		token, err := codec.Encode(claims, 12345)
		if err != nil {
			t.Fatal(err)
		}
		for _, at := range []time.Time{now, now.Add(7 * 24 * time.Hour), now.Add(-actionFutureSkew)} {
			got, err := codec.Decode(token, 12345, at)
			if err != nil || got.Nonce != nonce {
				t.Fatalf("nonce/lifetime changed: %+v %v", got, err)
			}
		}
		for _, at := range []time.Time{now.Add(7*24*time.Hour + time.Nanosecond), now.Add(-actionFutureSkew - time.Nanosecond)} {
			if got, err := codec.Decode(token, 12345, at); err == nil || got != (ActionClaims{}) {
				t.Fatalf("invalid lifetime: %+v %v", got, err)
			}
		}
		for _, id := range []int64{0, -12345} {
			if _, err := codec.Encode(claims, id); err == nil {
				t.Fatal("non-private chat encoded")
			}
			if _, err := codec.Decode(token, id, now); err == nil {
				t.Fatal("non-private chat decoded")
			}
		}
		// Even a correctly signed wire body cannot smuggle a zero nonce.
		wire, _ := base64.RawURLEncoding.DecodeString(token[len(actionPrefix):])
		body := wire[:actionBodySize]
		binary.BigEndian.PutUint64(body[8:16], 0)
		forged := actionPrefix + base64.RawURLEncoding.EncodeToString(append(body, codec.signature(body, 12345)...))
		if got, err := codec.Decode(forged, 12345, now); err == nil || got != (ActionClaims{}) {
			t.Fatal("zero nonce accepted")
		}
	}
}

func TestNotifierCredentialRotationAndKeyboardClaims(t *testing.T) {
	n := mustNew(t, Options{Token: syntheticToken, ChatID: "12345"})
	claims := ActionClaims{MAC: "02:00:00:00:00:01", Nonce: ^uint64(0), Issued: time.Unix(1790000000, 0)}
	keyboard, err := n.ActionKeyboard(claims)
	if err != nil {
		t.Fatal(err)
	}
	rotated := actionCodec(syntheticToken + "x")
	for i, button := range keyboard[0] {
		got, err := n.codec.Decode(button.Data, 12345, claims.Issued)
		want := []Action{ActionTrust, ActionBlockPrompt}[i]
		if err != nil || got.Action != want || got.Nonce != claims.Nonce || got.MAC != claims.MAC || !got.Issued.Equal(claims.Issued) {
			t.Fatalf("keyboard changed claims: %+v %v", got, err)
		}
		if _, err := rotated.Decode(button.Data, 12345, claims.Issued); err == nil {
			t.Fatal("old credential accepted")
		}
	}
}
