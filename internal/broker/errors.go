package broker

import "errors"

// ErrListResourcesNotImplemented is returned when a server does not implement the resources/list capability.
var ErrListResourcesNotImplemented = errors.New("list resources not implemented")
