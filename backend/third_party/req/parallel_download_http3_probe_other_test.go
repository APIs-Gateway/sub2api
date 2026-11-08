//go:build !windows

// Original test code is under this module's existing MIT license.
package req

import (
	"errors"
	"net"
	"syscall"
	"testing"
)

func parallelH3CleanupBindProbe(t *testing.T, endpoint *net.UDPAddr) (net.PacketConn, error) {
	t.Helper()
	// Preserve the original non-Windows exact peer-endpoint probe.
	return net.ListenUDP("udp4", endpoint)
}

func parallelH3CleanupAddressInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}
