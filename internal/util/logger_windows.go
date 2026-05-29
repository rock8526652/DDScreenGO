//go:build windows

package util

import (
	"syscall"
)

func init() {
	// STD_OUTPUT_HANDLE is -11
	handle, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE)
	if err != nil {
		return
	}
	var mode uint32
	if err := syscall.GetConsoleMode(handle, &mode); err != nil {
		return
	}
	// ENABLE_VIRTUAL_TERMINAL_PROCESSING is 0x0004
	mode |= 0x0004
	
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	setConsoleMode := kernel32.NewProc("SetConsoleMode")
	_, _, _ = setConsoleMode.Call(uintptr(handle), uintptr(mode))
}
