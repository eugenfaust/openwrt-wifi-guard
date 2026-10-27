package telegram

import (
	"bytes"
	"testing"
	"time"
)

func FuzzActionCodecDecode(f *testing.F) {
	codec, err := NewActionCodec(bytes.Repeat([]byte{0x42}, 32), time.Hour)
	if err != nil {
		f.Fatal(err)
	}
	now := time.Unix(1790000000, 0).UTC()
	valid, err := codec.Encode(ActionClaims{Action: ActionTrust, MAC: "02:00:00:00:00:01", Nonce: 42, Issued: now}, 12345)
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range []string{"", "wg1:", valid, valid + "\n", valid + "=", "wg2:" + valid[4:]} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		claims, err := codec.Decode(input, 12345, now)
		if err != nil {
			return
		}
		canonical, err := codec.Encode(claims, 12345)
		if err != nil || canonical != input || len(input) > 64 {
			t.Fatal("accepted noncanonical callback")
		}
	})
}
