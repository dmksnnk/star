package webserver

import "errors"

var errorMessages = []string{
	"GAME OVER! An error has appeared",
	"ERROR ENCOUNTERED! The princess is in another castle!",
	"SYSTEM MALFUNCTION! Insert coin to continue:",
	"Oops! You found a glitch in the matrix:",
	"ACHIEVEMENT UNLOCKED: Discover an error!",
	"404 MOOD: Something went wrong:",
}

var (
	ErrMissingSecret     = errors.New("secret is required")
	ErrMissingGame       = errors.New("game is required")
	ErrUnknownGame       = errors.New("unknown game")
	ErrInvalidPort       = errors.New("listen port must be between 1 and 65535")
	ErrMissingInviteCode = errors.New("invite code is required")
	ErrMissingName       = errors.New("name is required")
)
