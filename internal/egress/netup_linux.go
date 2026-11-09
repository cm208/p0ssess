package egress

import (
	"syscall"
	"unsafe"
)

// loopbackUp sets IFF_UP on lo. A fresh network namespace starts with loopback down.
func loopbackUp() error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	var req struct {
		name  [16]byte
		flags uint16
		_     [22]byte
	}
	copy(req.name[:], "lo")
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCGIFFLAGS, uintptr(unsafe.Pointer(&req))); e != 0 {
		return e
	}
	req.flags |= syscall.IFF_UP
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCSIFFLAGS, uintptr(unsafe.Pointer(&req))); e != 0 {
		return e
	}
	return nil
}
