package telegram

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net"
	"time"

	"openwrt-wifi-guard/internal/devices"
)

// Action tokens are opaque, chat-bound and fit Telegram's 64-byte callback_data
// limit. Signature validity is not authorization: the callback consumer must validate
// sender/chat/message and the owner must match the current device review nonce.
type Action byte

const (
	ActionTrust Action = iota + 1
	ActionBlockPrompt
	ActionBlockConfirm
	ActionUnblockPrompt
	ActionUnblockConfirm
	ActionCancel
)

type ActionClaims struct {
	Action Action
	MAC    string
	Nonce  uint64
	Issued time.Time
}
type ActionCodec struct {
	key    [32]byte
	maxAge time.Duration
}

var ErrInvalidAction = errors.New("invalid or expired action")

const actionPrefix = "wg1:"
const actionBodySize = 1 + 1 + 6 + 8 + 4
const actionSignatureSize = 16
const actionFutureSkew = 2 * time.Minute

func NewActionCodec(key []byte, maxAge time.Duration) (*ActionCodec, error) {
	if len(key) != 32 || maxAge <= 0 || maxAge > 30*24*time.Hour {
		return nil, ErrInvalidAction
	}
	codec := &ActionCodec{maxAge: maxAge}
	copy(codec.key[:], key)
	return codec, nil
}
func validAction(action Action) bool { return action >= ActionTrust && action <= ActionCancel }
func (c *ActionCodec) signature(body []byte, chatID int64) []byte {
	mac := hmac.New(sha256.New, c.key[:])
	_, _ = mac.Write([]byte("wifi-guard/action/v1"))
	_, _ = mac.Write(body)
	var chat [8]byte
	binary.BigEndian.PutUint64(chat[:], uint64(chatID))
	_, _ = mac.Write(chat[:])
	return mac.Sum(nil)[:actionSignatureSize]
}
func (c *ActionCodec) Encode(claims ActionClaims, chatID int64) (string, error) {
	normalized, err := devices.NormalizeMAC(claims.MAC)
	seconds := claims.Issued.Unix()
	if c == nil || !validAction(claims.Action) || err != nil || claims.Nonce == 0 || chatID <= 0 || seconds <= 0 || uint64(seconds) > uint64(^uint32(0)) {
		return "", ErrInvalidAction
	}
	address, err := net.ParseMAC(normalized)
	if err != nil {
		return "", ErrInvalidAction
	}
	body := make([]byte, actionBodySize, actionBodySize+actionSignatureSize)
	body[0] = 1
	body[1] = byte(claims.Action)
	copy(body[2:8], address)
	binary.BigEndian.PutUint64(body[8:16], claims.Nonce)
	binary.BigEndian.PutUint32(body[16:20], uint32(seconds))
	wire := append(body, c.signature(body, chatID)...)
	return actionPrefix + base64.RawURLEncoding.EncodeToString(wire), nil
}
func (c *ActionCodec) Decode(token string, chatID int64, now time.Time) (ActionClaims, error) {
	invalid := ActionClaims{}
	expectedLen := len(actionPrefix) + base64.RawURLEncoding.EncodedLen(actionBodySize+actionSignatureSize)
	if c == nil || chatID <= 0 || len(token) != expectedLen || token[:len(actionPrefix)] != actionPrefix {
		return invalid, ErrInvalidAction
	}
	encoded := token[len(actionPrefix):]
	wire, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(wire) != actionBodySize+actionSignatureSize || base64.RawURLEncoding.EncodeToString(wire) != encoded {
		return invalid, ErrInvalidAction
	}
	body := wire[:actionBodySize]
	if !hmac.Equal(wire[actionBodySize:], c.signature(body, chatID)) || body[0] != 1 || !validAction(Action(body[1])) {
		return invalid, ErrInvalidAction
	}
	address, err := devices.NormalizeMAC(net.HardwareAddr(body[2:8]).String())
	if err != nil {
		return invalid, ErrInvalidAction
	}
	claims := ActionClaims{Action: Action(body[1]), MAC: address, Nonce: binary.BigEndian.Uint64(body[8:16]), Issued: time.Unix(int64(binary.BigEndian.Uint32(body[16:20])), 0).UTC()}
	if claims.Nonce == 0 || claims.Issued.Unix() <= 0 || now.Sub(claims.Issued) > c.maxAge || claims.Issued.After(now.Add(actionFutureSkew)) {
		return invalid, ErrInvalidAction
	}
	return claims, nil
}
