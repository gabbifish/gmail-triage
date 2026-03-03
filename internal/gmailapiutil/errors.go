package gmailapiutil

import (
	"errors"
	"net"

	"google.golang.org/api/googleapi"
)

// IsAlreadyExists reports whether err represents a "resource already exists" API response.
// It is used for idempotent operations where duplicate creation attempts should be treated
// as a safe no-op instead of a hard failure.
func IsAlreadyExists(err error) bool {
	gerr, ok := err.(*googleapi.Error)
	if !ok {
		return false
	}
	return gerr.Code == 409
}

// IsRetriable reports whether err is likely transient and worth retrying.
// It treats common Gmail API rate/availability errors and temporary network failures
// as retriable so callers can apply bounded retry/backoff behavior.
func IsRetriable(err error) bool {
	if err == nil {
		return false
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		switch gerr.Code {
		case 429, 500, 502, 503, 504:
			return true
		}
	}
	var netErr net.Error
	return errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary())
}
