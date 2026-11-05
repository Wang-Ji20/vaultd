package main

import (
	"os"
	"syscall"
)

const enableEchoInput = 0x0004

// disableEcho hides typed characters and returns a function that restores
// them. If stdin is not a console, there is nothing to hide.
func disableEcho(stdin *os.File) (restore func()) {
	handle := syscall.Handle(stdin.Fd())
	var mode uint32
	if err := syscall.GetConsoleMode(handle, &mode); err != nil {
		return func() {}
	}
	setMode := syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleMode")
	setMode.Call(uintptr(handle), uintptr(mode&^enableEchoInput))
	return func() { setMode.Call(uintptr(handle), uintptr(mode)) }
}
