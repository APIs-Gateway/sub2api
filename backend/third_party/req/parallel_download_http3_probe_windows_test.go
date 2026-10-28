// These test-only probes use public Go socket controls and the documented
// Winsock wildcard/exclusive binding contract. They never replace the actual
// req/QUIC transport socket. Original test code is under this module's MIT license.
package req

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
	"testing"
)

func parallelH3CleanupBindProbe(t *testing.T, endpoint *net.UDPAddr) (net.PacketConn, error) {
	t.Helper()
	// Official WinSock2.h: SO_EXCLUSIVEADDRUSE is ((int)(~SO_REUSEADDR)).
	// A specific-address probe may bind beside a default wildcard Windows
	// socket even with this option. Use a wildcard probe for the actual port.
	const exclusiveAddressUse = ^syscall.SO_REUSEADDR
	config := net.ListenConfig{Control: func(network, address string, raw syscall.RawConn) error {
		if network != "udp4" && network != "udp6" {
			return fmt.Errorf("unexpected probe socket network %q", network)
		}
		var socketErr error
		value := 0
		controlErr := raw.Control(func(fd uintptr) {
			handle := syscall.Handle(fd)
			socketErr = syscall.SetsockoptInt(handle, syscall.SOL_SOCKET, exclusiveAddressUse, 1)
			if socketErr == nil {
				value, socketErr = syscall.GetsockoptInt(handle, syscall.SOL_SOCKET, exclusiveAddressUse)
			}
		})
		if controlErr != nil {
			return fmt.Errorf("exclusive probe RawConn.Control: %w", controlErr)
		}
		if socketErr != nil {
			return fmt.Errorf("exclusive probe socket option: %w", socketErr)
		}
		if value != 1 {
			return fmt.Errorf("exclusive probe socket option readback=%d, want1", value)
		}
		t.Logf("G1748_H3_WINDOWS_PROBE network=%s wildcard=%s exclusiveAddressUse=%d preBind=true", network, address, value)
		return nil
	}}
	return config.ListenPacket(context.Background(), "udp", ":"+strconv.Itoa(endpoint.Port))
}

func parallelH3CleanupAddressInUse(err error) bool {
	// Go syscall.EADDRINUSE is synthetic on Windows; Winsock's real bind
	// status is10048. EACCES10013 and every other failure remain unacceptable.
	var call *os.SyscallError
	return errors.As(err, &call) && call.Syscall == "bind" && errors.Is(call.Err, syscall.Errno(10048))
}
