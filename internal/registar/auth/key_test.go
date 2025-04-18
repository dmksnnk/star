package auth_test

import (
	"strings"
	"testing"

	"github.com/dmksnnk/star/internal/registar/auth"
)

func TestKey(t *testing.T) {
	t.Run("from text", func(t *testing.T) {
		tests := []struct {
			in  string
			out string
		}{
			{"0123-4567", "0123-4567"},
			{"01234567", "0123-4567"},
			// lowercase O
			{"o1234567", "0123-4567"},
			// uppercase O
			{"O1234567", "0123-4567"},
			// lowecase L
			{"0l234567", "0123-4567"},
			// uppercase L
			{"0L234567", "0123-4567"},
		}

		for _, test := range tests {
			t.Run(test.in, func(t *testing.T) {
				var key auth.Key
				if err := key.UnmarshalText([]byte(test.in)); err != nil {
					t.Fatal(err)
				}

				if key.String() != test.out {
					t.Errorf("want %q, got %q", test.out, key.String())
				}
			})
		}
	})

	t.Run("parse", func(t *testing.T) {
		key := auth.NewKey()
		key2, err := auth.ParseKey(key.String())
		if err != nil {
			t.Fatal(err)
		}
		if key != key2 {
			t.Errorf("want %v, got %v", key, key2)
		}
	})

	t.Run("from sql", func(t *testing.T) {
		t.Run("valid", func(t *testing.T) {
			key := auth.NewKey()
			v, err := key.Value()
			if err != nil {
				t.Fatal(err)
			}

			var key2 auth.Key
			if err := key2.Scan(v); err != nil {
				t.Fatal(err)
			}

			if key != key2 {
				t.Errorf("want %v, got %v", key, key2)
			}
		})

		t.Run("nil", func(t *testing.T) {
			var key auth.Key
			err := key.Scan(nil)
			if err != nil {
				t.Fatal(err)
			}

			if key != (auth.Key{}) {
				t.Errorf("want zero key, got %v", key)
			}
		})

		t.Run("invalid type", func(t *testing.T) {
			var key auth.Key
			err := key.Scan(42)
			if err == nil {
				t.Error("expected error")
				return
			}

			if !strings.Contains(err.Error(), "unable to scan type int into Key") {
				t.Errorf("want %q, got %q", "unable to scan type int into Key", err.Error())
			}
		})

		t.Run("invalid length", func(t *testing.T) {
			var key auth.Key
			err := key.Scan("eAo=") // "x" in base64
			if err == nil {
				t.Error("expected error")
				return
			}

			if !strings.Contains(err.Error(), "invalid length") {
				t.Errorf("want %q, got %q", "invalid length", err.Error())
			}
		})
	})
}
