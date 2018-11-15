// +build !race

package race

import "unsafe"

// Enabled represents the -race is enabled or not.
const Enabled = false

func Acquire(addr unsafe.Pointer)             {}
func Release(addr unsafe.Pointer)             {}
func ReleaseMerge(addr unsafe.Pointer)        {}
func Disable()                                {}
func Enable()                                 {}
func Read(addr unsafe.Pointer)                {}
func Write(addr unsafe.Pointer)               {}
func ReadRange(addr unsafe.Pointer, len int)  {}
func WriteRange(addr unsafe.Pointer, len int) {}
func Errors() int                             { return 0 }
