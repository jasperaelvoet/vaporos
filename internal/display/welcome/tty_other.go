//go:build !linux

package welcome

// platformSupported: there are no DRM screens to drive on the dev Mac; use
// `vos welcome --png out.png` there instead.
const platformSupported = false

func setGraphics(tty string, opts *Options) func() { return func() {} }
