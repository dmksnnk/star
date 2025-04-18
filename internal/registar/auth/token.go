package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

const TokenSize = KeySize + sha256.Size

// Token is a unique identifier for a game.
// It is a [Key] joined with 32-byte signature.
type Token [TokenSize]byte

// Key returns the key part of the token.
func (t Token) Key() Key {
	var key Key
	copy(key[:], t[:KeySize])
	return key
}

func (t Token) String() string {
	return base64.URLEncoding.EncodeToString(t[:])
}

func (t Token) MarshalText() ([]byte, error) {
	res := make([]byte, base64.URLEncoding.EncodedLen(len(t)))
	base64.URLEncoding.Encode(res, t[:])
	return res, nil
}

func (t *Token) UnmarshalText(text []byte) error {
	decoded := make([]byte, base64.URLEncoding.DecodedLen(len(text)))
	n, err := base64.URLEncoding.Decode(decoded, text)
	if err != nil {
		return err
	}

	if n != TokenSize {
		return fmt.Errorf("invalid token size %d", len(text))
	}

	copy(t[:], decoded)
	return nil
}

// ParseToken parses a token from a string.
func ParseToken(text string) (Token, error) {
	var token Token
	err := token.UnmarshalText([]byte(text))
	return token, err
}

// NewToken creates a new token by signing a key with a secret.
func NewToken(key Key, secret []byte) Token {
	var token Token
	copy(token[:KeySize], key[:])

	h := hmac.New(sha256.New, secret)
	h.Write(token[:KeySize]) // write first size bytes to hmac

	h.Sum(token[:KeySize]) // append the hmac to the token

	return token
}

// VerifyToken checks if a token is valid by checking its HMAC signature.
func VerifyToken(secret []byte, token Token) bool {
	h := hmac.New(sha256.New, secret)
	h.Write(token[:KeySize])

	expected := h.Sum(nil)
	return hmac.Equal(token[KeySize:], expected)
}
