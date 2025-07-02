// Package errcode provides a list of QUIC application error codes for
// communication between the game client and the game server.
package errcode

import (
	"errors"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// Application error codes.
const (
	Exit = http3.ErrCode(0)
)

// Stream error codes.
const (
	Unknown = quic.StreamErrorCode(iota + 1)
	HostClosed
	PeerClosed
	HostInternalError
	PeerInternalError
	Cancelled
)

// IsLocalQUICConnClosed returns true if the error is local QUIC stream closed.
func IsLocalQUICConnClosed(err error) bool {
	var appErr *http3.Error
	if errors.As(err, &appErr) {
		return !appErr.Remote && appErr.ErrorCode == Exit
	}
	return false
}

// IsRemoteQUICConnClosed returns true if the error is remote QUIC stream closed.
func IsRemoteQUICConnClosed(err error) bool {
	var appErr *http3.Error
	if errors.As(err, &appErr) {
		return appErr.Remote && appErr.ErrorCode == Exit
	}
	return false
}

// IsLocalStreamError returns true if the error is local QUIC stream error with the given code.
func IsLocalStreamError(err error, code quic.StreamErrorCode) bool {
	var streamErr *quic.StreamError
	if errors.As(err, &streamErr) {
		return !streamErr.Remote && streamErr.ErrorCode == code
	}
	return false
}

// IsLocalHTTPError returns true if the error is local HTTP/3 error with the given code.
func IsLocalHTTPError(err error, code quic.StreamErrorCode) bool {
	var http3Err *http3.Error
	if errors.As(err, &http3Err) {
		return !http3Err.Remote && http3Err.ErrorCode == http3.ErrCode(code)
	}

	return false
}

// IsRemoteStreamError returns true if the error is remote QUIC stream error with the given code.
func IsRemoteStreamError(err error, code quic.StreamErrorCode) bool {
	var streamErr *quic.StreamError
	if errors.As(err, &streamErr) {
		return streamErr.Remote && streamErr.ErrorCode == code
	}
	return false
}
