package webserver

import (
	"fmt"
	"net/url"

	"github.com/dmksnnk/star/internal/registar/auth"
)

type ClientConfig struct {
	Secret string `form:"secret"`
	URL    string `form:"url"`
	CACert string `form:"ca-cert"`
}

func (c ClientConfig) Validate() error {
	if c.Secret == "" {
		return ErrMissingSecret
	}

	if c.URL != "" {
		if _, err := url.Parse(c.URL); err != nil {
			return fmt.Errorf("invalid URL: %w", err)
		}
	}

	return nil
}

// ConnectRequest is the request structure for connecting to a game server.
type ConnectRequest struct {
	ClientConfig

	InviteCode auth.Key `form:"invite-code"`
	Name       string   `form:"name"`
}

func (r *ConnectRequest) Validate() error {
	if err := r.ClientConfig.Validate(); err != nil {
		return err
	}

	if r.InviteCode == (auth.Key{}) {
		return ErrMissingInviteCode
	}

	if r.Name == "" {
		return ErrMissingName
	}

	return nil
}

// StartGameServerRequest is the request structure for starting a game server.
type StartGameServerRequest struct {
	ClientConfig

	GamePort int `form:"game-port"`
}

func (r StartGameServerRequest) Validate() error {
	if err := r.ClientConfig.Validate(); err != nil {
		return err
	}

	if r.GamePort < 0 || r.GamePort > 65535 {
		return ErrInvalidPort
	}

	return nil
}
