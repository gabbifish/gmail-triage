package gmailapiutil

import (
	"errors"
	"net"

	"google.golang.org/api/googleapi"
)

func IsAlreadyExists(err error) bool {
	gerr, ok := err.(*googleapi.Error)
	if !ok {
		return false
	}
	return gerr.Code == 409
}

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
