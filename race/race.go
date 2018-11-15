// +build race

package race

import (
	"runtime"
	"unsafe"
)

// Enabled represents the -race is enabled or not.
const Enabled = true

func Acquire(addr unsafe.Pointer)             { runtime.RaceAcquire(addr) }
func Release(addr unsafe.Pointer)             { runtime.RaceRelease(addr) }
func ReleaseMerge(addr unsafe.Pointer)        { runtime.RaceReleaseMerge(addr) }
func Disable()                                { runtime.RaceDisable() }
func Enable()                                 { runtime.RaceEnable() }
func Read(addr unsafe.Pointer)                { runtime.RaceRead(addr) }
func Write(addr unsafe.Pointer)               { runtime.RaceWrite(addr) }
func ReadRange(addr unsafe.Pointer, len int)  { runtime.RaceReadRange(addr, len) }
func WriteRange(addr unsafe.Pointer, len int) { runtime.RaceWriteRange(addr, len) }
func Errors() int                             { return runtime.RaceErrors() }
