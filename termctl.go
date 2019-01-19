package main

// struct winsize {
//     unsigned short ws_row;
//     unsigned short ws_col;
//     unsigned short ws_xpixel;   /* unused */
//     unsigned short ws_ypixel;   /* unused */
// };
import "C"

import (
	"github.com/pkg/term/termios"
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

func termSetSize(fd uintptr, sz sizeStruct) {
	wsize := C.struct_winsize{}
	wsize.ws_row = C.ushort(sz.rows)
	wsize.ws_col = C.ushort(sz.cols)
	syscall.Syscall6(syscall.SYS_IOCTL, fd, syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&wsize)), 0, 0, 0)
}

func termSetRaw() syscall.Termios {
	ttyAttr := syscall.Termios{}
	termios.Tcgetattr(0, &ttyAttr)
	copy := ttyAttr
	copy.Iflag &= ^uint32(syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON)
	copy.Oflag &= ^uint32(syscall.OPOST)
	copy.Lflag &= ^uint32(syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN)
	copy.Cflag |= uint32(syscall.CS8)
	termios.Tcsetattr(0, termios.TCSADRAIN, &copy)
	return ttyAttr
}

func termRestore(attr syscall.Termios) {
	termios.Tcsetattr(0, termios.TCSADRAIN, &attr)
}

func tiocsctty(fd uintptr) {
	syscall.Syscall6(syscall.SYS_IOCTL, fd, syscall.TIOCSCTTY, 0, 0, 0, 0)
}
