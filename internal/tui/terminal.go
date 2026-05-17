package tui

import (
	"os"
	"syscall"
	"unsafe"
)

// winsize matches the kernel struct for TIOCGWINSZ.
type winsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

// termios matches the kernel termios struct for TCGETS/TCSETS.
type termios struct {
	Iflag  uint32
	Oflag  uint32
	Cflag  uint32
	Lflag  uint32
	Line   uint8
	Cc     [32]uint8
	Ispeed uint32
	Ospeed uint32
}

const (
	ioctlTIOCGWINSZ = 0x5413
	ioctlTCGETS     = 0x5401
	ioctlTCSETS     = 0x5402
)

// termios local flags
const (
	tECHO   uint32 = 0x00000008
	tICANON uint32 = 0x00000002
	tISIG   uint32 = 0x00000001
)

// termios input flags
const (
	tIXON  uint32 = 0x00000400
	tICRNL uint32 = 0x00000100
)

// GetTerminalSize returns the current terminal dimensions (rows, cols).
// Falls back to 40x120 if ioctl fails.
func GetTerminalSize() (rows, cols int) {
	ws := &winsize{}
	fd := os.Stdout.Fd()
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(ioctlTIOCGWINSZ), uintptr(unsafe.Pointer(ws)))
	if errno != 0 || ws.Row == 0 || ws.Col == 0 {
		return 40, 120
	}
	return int(ws.Row), int(ws.Col)
}

// EnableRawMode puts the terminal into raw mode (no echo, no canonical, no signals from input).
// Returns a restore function that must be called to reset the terminal, and any error.
func EnableRawMode() (restore func(), err error) {
	fd := os.Stdin.Fd()

	var orig termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(ioctlTCGETS), uintptr(unsafe.Pointer(&orig)))
	if errno != 0 {
		return nil, errno
	}

	raw := orig
	// Disable echo, canonical mode, and signal generation
	raw.Lflag &^= tECHO | tICANON | tISIG
	// Disable input processing
	raw.Iflag &^= tIXON | tICRNL
	// Min bytes = 0, timeout = 0 (non-blocking)
	raw.Cc[6] = 0  // VMIN
	raw.Cc[5] = 0  // VTIME

	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(ioctlTCSETS), uintptr(unsafe.Pointer(&raw)))
	if errno != 0 {
		return nil, errno
	}

	restore = func() {
		syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(ioctlTCSETS), uintptr(unsafe.Pointer(&orig)))
	}
	return restore, nil
}
