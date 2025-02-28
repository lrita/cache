//go:build !go1.23
// +build !go1.23

package cache

import _ "unsafe"

//go:linkname fastrand runtime.fastrand
func fastrand() uint32
