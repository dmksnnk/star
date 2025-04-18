package auth_test

import (
	"crypto/rand"
	"testing"

	"github.com/dmksnnk/star/internal/registar/auth"
)

func TestVerifyToken(t *testing.T) {
	secret := make([]byte, 32)
	rand.Read(secret)
	t.Run("valid", func(t *testing.T) {
		key := auth.NewKey()
		token := auth.NewToken(key, secret)
		if token.Key() != key {
			t.Errorf("key is not valid")
		}

		if !auth.VerifyToken(secret, token) {
			t.Errorf("token is not valid")
		}
	})

	t.Run("invalid", func(t *testing.T) {
		key := auth.NewKey()
		token := auth.NewToken(key, secret)

		secret2 := make([]byte, 32)
		rand.Read(secret2)
		if auth.VerifyToken(secret2, token) {
			t.Errorf("token is not valid")
		}
	})
}
