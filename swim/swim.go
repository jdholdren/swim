package swim

import (
	"context"
	"fmt"
	"net"
)

// Swim is an instance of the swim agent
type Swim struct {
	IP net.UDPAddr // Our own IP

	// TODO: Need a lock to edit these
	// List of good members
	members []net.UDPAddr

	// List of bad members
	removed []net.UDPAddr
}

// Listen opens a port and sends signals
func (s *Swim) Listen(ctx context.Context) error {
	addr := net.UDPAddr{
		Port: 4444,
		IP:   net.ParseIP("127.0.0.1"),
	}
	_, err := net.ListenUDP("udp", &addr)
	if err != nil {
		return fmt.Errorf("could not open connection: %s", err)
	}

	// TODO: Start a listener loop
	return nil
}
