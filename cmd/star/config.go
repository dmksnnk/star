package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"

	"github.com/dmksnnk/star/internal/registar/auth"
)

const stardewPort = 24642

const commandsUsage = `
Commands:
  host - run the game host
  peer - run the game peer`

type commandConfig struct {
	FS *flag.FlagSet

	Command  string
	Secret   string
	Registar textURL
	Port     int
	CaCert   string
}

func (c *commandConfig) Parse(args []string) error {
	c.FS = flag.NewFlagSet("star", flag.ExitOnError)
	c.FS.StringVar(&c.Secret, "secret", "", "secret key (required)")
	c.FS.TextVar(&c.Registar, "registar", textURL{}, "registar URL")
	c.FS.IntVar(&c.Port, "port", 0, "port to listen on, if not set, listen on system assigned port")
	c.FS.StringVar(&c.CaCert, "ca-cert", "", "path to CA certificate for registar")
	c.FS.Usage = func() {
		fmt.Fprintln(c.FS.Output()) // newline
		fmt.Fprintln(c.FS.Output(), "Usage: star [OPTIONS] COMMAND")
		fmt.Fprintln(c.FS.Output(), commandsUsage)

		fmt.Fprintln(c.FS.Output()) // newline
		fmt.Fprintln(c.FS.Output(), "Global options:")
		c.FS.PrintDefaults()
	}

	if err := c.FS.Parse(args); err != nil {
		return err
	}

	if len(c.FS.Args()) < 1 {
		return errors.New("missing command")
	}

	c.Command = c.FS.Args()[0]

	if c.Secret == "" {
		return errors.New("missing secret")
	}

	if c.Registar.URL == nil {
		return errors.New("missing registar URL")
	}

	return nil
}

type hostConfig struct {
	FS       *flag.FlagSet
	GamePort int
	GameKey  auth.Key
}

func (c *hostConfig) Parse(args []string) error {
	c.FS = flag.NewFlagSet("host", flag.ExitOnError)
	c.FS.Usage = func() {
		fmt.Fprintln(c.FS.Output())
		fmt.Fprintln(c.FS.Output(), "Usage: star host [OPTIONS]")
		fmt.Fprintln(c.FS.Output(), "Options:")
		c.FS.PrintDefaults()
	}

	c.FS.IntVar(&c.GamePort, "port", stardewPort, "the port on which game host listens")
	c.FS.TextVar(&c.GameKey, "key", auth.Key{}, "the key of the game to register. If not set, a new key will be generated")

	if err := c.FS.Parse(args); err != nil {
		return err
	}

	if c.GameKey == (auth.Key{}) {
		c.GameKey = auth.NewKey()
	}

	return nil
}

type peerConfig struct {
	FS             *flag.FlagSet
	Key            auth.Key
	GameListenPort int
}

func (c *peerConfig) Parse(args []string) error {
	c.FS = flag.NewFlagSet("peer", flag.ExitOnError)
	c.FS.Usage = func() {
		fmt.Fprintln(c.FS.Output())
		fmt.Fprintln(c.FS.Output(), "Usage: star peer [OPTIONS]")
		fmt.Fprintln(c.FS.Output(), "Options:")
		c.FS.PrintDefaults()
	}

	c.FS.TextVar(&c.Key, "key", auth.Key{}, "the key of the game to connect to (required)")
	c.FS.IntVar(&c.GameListenPort, "listen-port", 0, "the port on which listen for game traffic. If 0, a random port will be chosen.")

	if err := c.FS.Parse(args); err != nil {
		return err
	}

	if c.Key == (auth.Key{}) {
		return errors.New("missing game key")
	}

	return nil
}

func abort(fs *flag.FlagSet, err error) {
	fmt.Fprintf(fs.Output(), "Error: %v\n", err)
	fs.Usage()
	os.Exit(2)
}

type textURL struct {
	*url.URL
}

func (u *textURL) UnmarshalText(text []byte) error {
	parsed, err := url.Parse(string(text))
	if err != nil {
		return err
	}
	*u = textURL{parsed}
	return nil
}

func (u textURL) MarshalText() ([]byte, error) {
	if u.URL == nil {
		return nil, nil
	}
	return []byte(u.String()), nil
}
