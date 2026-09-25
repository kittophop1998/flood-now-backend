// Package clock provides the real-time implementation of ports.Clock.
package clock

import "time"

type Real struct{}

func (Real) Now() time.Time { return time.Now().UTC() }
