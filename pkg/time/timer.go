package pkgtime

import "time"

// NewTimer is like time.NewTimer() but also returns a wrapper for:
//
// 	if !t.Stop() {
// 		<-t.C
// 	}
func NewTimer(d time.Duration) (*time.Timer, func()) {
	t := time.NewTimer(d)
	return t, func() {
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
	}
}
