package main

// struct winsize {
//     unsigned short ws_row;
//     unsigned short ws_col;
//     unsigned short ws_xpixel;   /* unused */
//     unsigned short ws_ypixel;   /* unused */
// };
import "C"

import (
	"syscall"
	"unsafe"
)

func termGetSize() (sz sizeStruct) {
	wsize := C.struct_winsize{}
	syscall.Syscall6(syscall.SYS_IOCTL, 0, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&wsize)), 0, 0, 0)
	sz.rows = int(wsize.ws_row)
	sz.cols = int(wsize.ws_col)
	return
}
