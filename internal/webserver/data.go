package webserver

import "net/url"

// PageData is an interface for all page data for rendering pages.
type PageData interface {
	SetCache(values url.Values)
}

// GameEntry represents a game entry in the catalog.
type GameEntry struct {
	ID   string
	Name string
	Port int
}

// GameCatalog is a list for known games
type GameCatalog []GameEntry

func (gs GameCatalog) GetByID(id string) (GameEntry, bool) {
	for _, game := range gs {
		if game.ID == id {
			return game, true
		}
	}

	return GameEntry{}, false
}

// InputCache is a cache for the form data, so we can
// repopulate the form with the previous values.
type InputCache url.Values

func (c *InputCache) SetCache(values url.Values) {
	*c = InputCache(values)
}

func (c InputCache) Get(key string) string {
	return url.Values(c).Get(key)
}

// StartGameServerPageData is the data structure for the start game server page.
type StartGameServerPageData struct {
	InputCache

	Errors   []error
	Messages []string
	Games    GameCatalog
}

type AdvancedPageData struct {
	InputCache

	URLs    []string
	CAFiles []string
}

// StartGameServerAdvancedPageData is the data structure for the advanced start game server page.
type StartGameServerAdvancedPageData struct {
	AdvancedPageData

	Errors   []error
	Messages []string
	Games    GameCatalog
}

// RunningServerPageData is the data structure for the running server page.
type RunningServerPageData struct {
	InputCache

	GameAddress string
	InviteCode  string
}

// ConnectPageData is the data structure for the connect page.
type ConnectPageData struct {
	AdvancedPageData

	Errors   []error
	Messages []string
}

type ConnectedPageData struct {
	InputCache

	ListenAddress string
}

// ErrorPageData is the data structure for the error page.
type ErrorPageData struct {
	InputCache

	BackLink string
	Message  string
	Error    string
}
