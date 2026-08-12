//go:build linux

package qmi

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ============================================================================
// qrtrRawSocket: minimal AF_QIPCRTR socket operations needed by qrtrTransport
//
// golang.org/x/sys/unix defines AF_QIPCRTR but has no SockaddrQIPCRTR type
// implementing the (unexported) unix.Sockaddr interface, so unix.Sendto /
// unix.Recvfrom cannot be used directly. This file talks to the kernel via
// raw syscalls with a hand-packed struct sockaddr_qrtr (see qrtr_wire.go).
// ============================================================================

// qrtrRawSocket abstracts the kernel socket calls qrtrTransport depends on so
// that tests can substitute an in-memory fake instead of requiring a real
// AF_QIPCRTR-capable kernel (CONFIG_QRTR).
type qrtrRawSocket interface {
	// LocalAddr returns the address the kernel auto-assigned to this socket.
	LocalAddr() (sockaddrQRTR, error)
	// SendTo sends data to dst.
	SendTo(data []byte, dst sockaddrQRTR) error
	// RecvFrom blocks (up to the configured receive timeout) for one datagram.
	// A timeout is reported via unix.EAGAIN/unix.EWOULDBLOCK.
	RecvFrom(buf []byte) (n int, from sockaddrQRTR, err error)
	// SetRecvTimeout bounds how long RecvFrom blocks.
	SetRecvTimeout(d time.Duration) error
	Close() error
}

// newQRTRRawSocket opens a real AF_QIPCRTR/SOCK_DGRAM kernel socket.
func newQRTRRawSocket() (qrtrRawSocket, error) {
	fd, err := unix.Socket(qrtrAddressFamily, unix.SOCK_DGRAM, 0)
	if err != nil {
		return nil, fmt.Errorf("qrtr: socket(AF_QIPCRTR, SOCK_DGRAM): %w", err)
	}
	return &qrtrKernelSocket{fd: fd}, nil
}

type qrtrKernelSocket struct {
	fd int
}

func bufPtr(b []byte) unsafe.Pointer {
	if len(b) == 0 {
		return nil
	}
	return unsafe.Pointer(&b[0])
}

func (s *qrtrKernelSocket) LocalAddr() (sockaddrQRTR, error) {
	var addrBuf [sockaddrQRTRSize]byte
	addrLen := uint32(sockaddrQRTRSize)

	_, _, errno := unix.Syscall(
		unix.SYS_GETSOCKNAME,
		uintptr(s.fd),
		uintptr(unsafe.Pointer(&addrBuf[0])),
		uintptr(unsafe.Pointer(&addrLen)),
	)
	if errno != 0 {
		return sockaddrQRTR{}, fmt.Errorf("qrtr: getsockname: %w", errno)
	}
	return unmarshalSockaddrQRTR(addrBuf[:])
}

func (s *qrtrKernelSocket) SendTo(data []byte, dst sockaddrQRTR) error {
	addr := marshalSockaddrQRTR(dst)
	_, _, errno := unix.Syscall6(
		unix.SYS_SENDTO,
		uintptr(s.fd),
		uintptr(bufPtr(data)),
		uintptr(len(data)),
		0, // flags
		uintptr(unsafe.Pointer(&addr[0])),
		uintptr(len(addr)),
	)
	if errno != 0 {
		return fmt.Errorf("qrtr: sendto: %w", errno)
	}
	return nil
}

func (s *qrtrKernelSocket) RecvFrom(buf []byte) (int, sockaddrQRTR, error) {
	var addrBuf [sockaddrQRTRSize]byte
	addrLen := uint32(sockaddrQRTRSize)

	n, _, errno := unix.Syscall6(
		unix.SYS_RECVFROM,
		uintptr(s.fd),
		uintptr(bufPtr(buf)),
		uintptr(len(buf)),
		0, // flags
		uintptr(unsafe.Pointer(&addrBuf[0])),
		uintptr(unsafe.Pointer(&addrLen)),
	)
	if errno != 0 {
		return 0, sockaddrQRTR{}, errno
	}
	from, err := unmarshalSockaddrQRTR(addrBuf[:])
	if err != nil {
		from = sockaddrQRTR{}
	}
	return int(n), from, nil
}

func (s *qrtrKernelSocket) SetRecvTimeout(d time.Duration) error {
	tv := unix.NsecToTimeval(d.Nanoseconds())
	return unix.SetsockoptTimeval(s.fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
}

func (s *qrtrKernelSocket) Close() error {
	return unix.Close(s.fd)
}

// isQRTRRetryable reports whether a RecvFrom error is a transient condition
// (receive-timeout expiry from SO_RCVTIMEO, or a signal interruption).
func isQRTRRetryable(err error) bool {
	return errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR)
}
