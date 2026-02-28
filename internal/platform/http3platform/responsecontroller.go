package http3platform

import (
	"net/http"

	"github.com/quic-go/quic-go/http3"
)

// A ResponseController is used by an HTTP handler to control the response.
type ResponseController struct {
	rw http.ResponseWriter
}

type rwUnwrapper interface {
	Unwrap() http.ResponseWriter
}

// NewResponseController creates a [ResponseController] for a HTTP/3 request.
//
// If the ResponseWriter implements any of the following methods, the ResponseController
// will call them as appropriate:
//
//	HTTPStream() *http3.Stream
//
// If the ResponseWriter does not support a method, ResponseController returns
// an error matching [http.ErrNotSupported].
func NewResponseController(rw http.ResponseWriter) *ResponseController {
	return &ResponseController{rw: rw}
}

// HTTPStream overtakes the HTTP/3 stream. See [http3.HTTPStreamer] for details.
func (c *ResponseController) HTTPStream() (*http3.Stream, error) {
	rw := c.rw
	for {
		switch t := rw.(type) {
		case http3.HTTPStreamer:
			return t.HTTPStream(), nil
		case rwUnwrapper:
			rw = t.Unwrap()
		default:
			return nil, http.ErrNotSupported
		}
	}
}
