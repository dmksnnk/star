package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql/driver"
	"encoding/base32"
	"errors"
	"fmt"
	"unicode"
)

type invalidLengthError int

func (e invalidLengthError) Error() string {
	return fmt.Sprintf("invalid length %d", int(e))
}

// ErrInvalidKeyFormat is returned when the key format is invalid.
var ErrInvalidKeyFormat = errors.New("invalid key format")

// KeySize is divisible by 5, so we can use base32 encoding without padding.
const KeySize = 5

// using crocford encoding https://www.crockford.com/base32.html
var encoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ")

// crocfordMap is a mapping function for the crocford encoding with special cases.
var crocfordMap = func(r rune) rune {
	switch r {
	case '-', '_': // skip
		return -1
	case '0', 'o', 'O':
		return '0'
	case '1', 'l', 'L':
		return '1'
	default:
		return unicode.ToUpper(r)
	}
}

// Key is a random 16-byte value.
// Used as a unique identifier.
type Key [KeySize]byte

func (k Key) String() string {
	text, _ := k.MarshalText()
	return string(text)
}

func (k *Key) UnmarshalText(text []byte) error {
	switch len(text) {
	case 8: // xxxxxxxx
	case 9: // xxxx-xxxx
		if text[4] != '-' {
			return ErrInvalidKeyFormat
		}
	default:
		return invalidLengthError(len(text))
	}

	text = bytes.Map(crocfordMap, text)
	decoded := make([]byte, KeySize)
	n, err := encoding.Decode(decoded, text)
	if err != nil {
		return err
	}

	return k.UnmarshalBinary(decoded[:n])
}

func (k Key) MarshalText() ([]byte, error) {
	encoded := make([]byte, 8)
	encoding.Encode(encoded, k[:])

	res := make([]byte, 9)
	copy(res[:4], encoded[:4])
	res[4] = '-'
	copy(res[5:], encoded[4:])

	return res, nil
}

func (k *Key) UnmarshalBinary(data []byte) error {
	if len(data) != KeySize {
		return invalidLengthError(len(data))
	}

	copy(k[:], data)
	return nil
}

func (k Key) MarshalBinary() ([]byte, error) {
	if k == (Key{}) {
		return nil, nil
	}
	return k[:], nil
}

func (k *Key) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		return k.UnmarshalText([]byte(v))
	case []byte:
		return k.UnmarshalBinary(v)
	default:
		return fmt.Errorf("unable to scan type %T into Key", v)
	}
}

func (k Key) Value() (driver.Value, error) {
	return k.String(), nil
}

// NewKey creates a new random key.
func NewKey() Key {
	var key Key
	if _, err := rand.Read(key[:]); err != nil {
		panic(err)
	}
	return key
}

// ParseKey parses a key from a string.
func ParseKey(text string) (Key, error) {
	var key Key
	err := key.UnmarshalText([]byte(text))
	return key, err
}

type ctxKey string

var keyCtxKey = ctxKey("key")

// ContextWithKey returns a new context with the given key.
func ContextWithKey(ctx context.Context, key Key) context.Context {
	return context.WithValue(ctx, keyCtxKey, key)
}

// KeyFromContext returns a key from the context.
func KeyFromContext(ctx context.Context) (Key, bool) {
	key, ok := ctx.Value(keyCtxKey).(Key)
	return key, ok
}
