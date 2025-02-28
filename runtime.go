//go:build go1.23
// +build go1.23

// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
package cache

import _ "unsafe"

//go:linkname fastrand runtime.cheaprand
//go:nosplit
func fastrand() uint32
