package webserver

import (
	"fmt"

	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/go-playground/form/v4"
)

func newFormDecoder() *form.Decoder {
	decoder := form.NewDecoder()
	decoder.RegisterCustomTypeFunc(func(s []string) (interface{}, error) {
		if len(s) == 0 {
			return nil, nil
		}
		var key auth.Key
		if err := key.UnmarshalText([]byte(s[0])); err != nil {
			return nil, fmt.Errorf("invalid key: %w", err)
		}
		return key, nil
	}, auth.Key{})

	return decoder
}
