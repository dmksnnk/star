package httpplatform

import (
	"errors"
	"io"
	"net/http"
	"strconv"
)

// BadStatusCodeError is returned when response status code is not OK (200).
type BadStatusCodeError struct {
	StatusCode int
	Body       []byte
}

func NewBadStatusCodeError(statusCode int, body io.Reader) *BadStatusCodeError {
	return &BadStatusCodeError{
		StatusCode: statusCode,
		Body:       tryReadBody(body),
	}
}

func (e *BadStatusCodeError) Error() string {
	return "bad status code: " + strconv.Itoa(e.StatusCode) + ": " + string(e.Body)
}

func tryReadBody(r io.Reader) []byte {
	data, _ := io.ReadAll(io.LimitReader(r, 256))
	return data
}

// IsUnauthorized returns true if the error is a BadStatusCodeError with status code 401 (Unauthorized).
func IsUnauthorized(err error) bool {
	var e *BadStatusCodeError
	return errors.As(err, &e) && e.StatusCode == http.StatusUnauthorized
}

// IsNotFound returns true if the error is a BadStatusCodeError with status code 404 (Not Found).
func IsNotFound(err error) bool {
	var e *BadStatusCodeError
	return errors.As(err, &e) && e.StatusCode == http.StatusNotFound
}
