//go:build !linux

package egress

import "errors"

func loopbackUp() error { return errors.New("shell internet needs Linux") }
