package swim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/oklog/run"
	"go.uber.org/zap"
)

var (
	ErrInterrupt = errors.New("interrupt signal received")
	ErrTimeout   = errors.New("operation timed out")
)

type Member struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
}

// Represents a packet and its data
type packet struct {
	remote *net.UDPAddr
	length int
	byts   []byte
}

type msgType uint

const (
	msgTypePing msgType = iota
	msgTypeAck
)

// The overarching type
type message struct {
	UUID    string   `json:"uuid"`
	Type    msgType  `json:"msgType"`
	Port    int      `json:"senderPort"`
	Members []Member `json:"members"`
}

type Config struct {
	Name         string   `env:"NAME,default=unnamed"`
	ReceiverPort int      `env:"RECEIVING_PORT,default=4444"`
	MessageSize  int      `env:"MESSAGE_SIZE,default=1024"`
	PingInterval int      `env:"PING_INTERVAL,default=200"` // The ping interval in ms
	K            int      `env:"K,default=3"`               // How many people to ping for failure detection
	InitialList  []string `env:"INITIAL_LIST"`
}

type memberMap map[string]Member

func (m memberMap) Slice() []Member {
	ret := make([]Member, 0, len(m))
	for _, member := range m {
		ret = append(ret, member)
	}

	return ret
}

// Swim is an instance of the swim agent
type Swim struct {
	cfg Config // IDK if i just like embedding this into the singleton
	l   *zap.SugaredLogger

	// This area deals with the member list itself
	membersLock sync.RWMutex
	members     memberMap // An indexed set of the members
}

func New(cfg Config) (*Swim, error) {
	l := zap.NewNop()

	mems := memberMap{}
	for _, ip := range cfg.InitialList {
		// Parse the pieces ip to make sure it's good
		parts := strings.Split(ip, ":")
		if p := net.ParseIP(parts[0]); p == nil {
			return nil, fmt.Errorf("error parsing ip: %s", ip)
		}
		if len(parts) < 2 {
			return nil, fmt.Errorf("port not provided on ip: %s", ip)
		}
		if _, err := strconv.Atoi(parts[1]); err != nil {
			return nil, fmt.Errorf("port was not a number: %s", ip)
		}

		mems[ip] = Member{
			IP: ip,
		}
	}

	if cfg.Name == "" {
		cfg.Name = "swmnode-" + uuid.NewString()
	}

	return &Swim{
		cfg:     cfg,
		l:       l.Sugar().With("name", cfg.Name),
		members: mems,
	}, nil
}

// Members returns a list of members according to this node.
// It's safe to call this method concurrently, but it may time out
func (s *Swim) Members(ctx context.Context) ([]Member, error) {
	resp := make(chan []Member)

	go func() {
		s.membersLock.RLock()
		defer s.membersLock.RUnlock()

		// This routine may be running after the context has been canceled,
		// so the channel may be closed: do a non-blocking send on the channel
		select {
		case resp <- s.members.Slice():
		default:
		}
	}()

	// This blocks until the lock is acquired or until the context is done:
	var mems []Member
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("timed out trying to read members: %w", ErrTimeout)
	case mems = <-resp:
	}

	return mems, nil
}

// Listen starts all components of the swimmer.
// It blocks until error or canceled.
func (s *Swim) Listen(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	pcktReceived := make(chan packet)
	defer close(pcktReceived)

	// Start each piece
	var g run.Group
	// The receiver
	g.Add(func() error {
		return s.receiveMessages(ctx, pcktReceived)
	}, func(err error) {
		cancel()
	})
	// The pinger
	g.Add(func() error {
		return s.sendPings(ctx)
	}, func(err error) {
		cancel()
	})

	// The event loop itself
	g.Add(func() error {
		return s.eventLoop(ctx, pcktReceived)
	}, func(err error) {
		cancel()
	})

	return g.Run()
}

func (s *Swim) eventLoop(ctx context.Context, pcktReceived <-chan packet) error {
	// The event loop here to respond to everything
	for {
		select {
		case <-ctx.Done():
			return ErrInterrupt
		case p := <-pcktReceived:
			go s.handlePacket(p)
			// TODO: Add for ping/ping-req received
		}
	}

	return nil
}

// Opens a connection on the port and emits messages on the channel
func (s *Swim) receiveMessages(ctx context.Context, out chan<- packet) error {
	addr := &net.UDPAddr{
		Port: s.cfg.ReceiverPort,
		IP:   net.ParseIP("127.0.0.1"),
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("could not open connection: %s", err)
	}

	// This is the channel that receives packets from the socket
	pCh := make(chan packet)
	// defer close(pCh) // Close your channels

	// Start a loop for receiving
	go func() {
		for {
			b := make([]byte, s.cfg.MessageSize)
			// Receive a message
			l, remoteAddr, err := conn.ReadFromUDP(b)
			if err != nil {
				// TODO: Do something with the error
				fmt.Printf("error reading from udp: %s", err)
			}

			// Log out the message
			go func() {
				pCh <- packet{
					remote: remoteAddr,
					byts:   b,
					length: l,
				}
			}()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ErrInterrupt
		case p := <-pCh:
			// Pump it out
			go func() {
				out <- p
			}()
		}
	}

	return nil
}

// Does all the things with the message:
// - Decodes the message
// - Checks the encryption
// - Updates internal state (member list, lamport clocks)
// - Sends out the correct channel
func (s *Swim) handlePacket(p packet) {
	// Log for now
	content := p.byts[:p.length]
	// s.l.Debugw("handlePacket",
	// 	"remote", fmt.Sprintf("%s:%d", p.remote.IP.String(), p.remote.Port),
	// 	"content", string(content),
	// )

	var msg message
	if err := json.Unmarshal(content, &msg); err != nil {
		s.l.Errorw("error unmarshaling packet", "err", err)
		return
	}

	s.membersLock.Lock()
	defer s.membersLock.Unlock()

	// Add who's pinging
	s.members[msg.UUID] = Member{
		Name: msg.UUID,
		IP:   fmt.Sprintf("%s:%d", p.remote.IP.String(), msg.Port),
	}

	// Use their piggy back info to update the list
	for _, m := range msg.Members {
		if m.Name == s.cfg.Name {
			continue // Don't add ourselves
		}

		s.members[m.Name] = Member{
			Name: m.Name,
			IP:   m.IP,
		}
	}

	// Clear any nameless entries, they are from the start
	for k, v := range s.members {
		if v.Name == "" {
			delete(s.members, k)
		}
	}
}

// Is a random timer that pings k other nodes on that interval
func (s *Swim) sendPings(ctx context.Context) error {
	for {
		t := time.NewTimer(time.Duration(s.cfg.PingInterval) * time.Millisecond)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			// Timer goes off, allow it to fall through
		}

		// Get a snapshot of the current members to determine who to ping
		s.membersLock.RLock()
		mems := s.members.Slice()
		s.membersLock.RUnlock()
		l := len(mems)

		s.l.Debugw("about to select members", "mems", mems)

		// Select k members in the list
		var toPing []Member
		if l == 0 {
			continue
		}
		toPing = mems
		// if l <= s.cfg.K {
		// 	// we can ping all of them, k encompasses the whole list
		// } else {
		// 	// Select a root number to select the next 3
		// 	root := rand.Intn(l)
		// 	for i := 0; i < s.cfg.K; i++ {
		// 		n := (i + root) % l // Get a safe index to access
		// 		toPing = append(toPing, mems[n])
		// 	}
		// }

		for _, mem := range toPing {
			go func() error {
				conn, err := net.Dial("udp", mem.IP)
				if err != nil {
					return fmt.Errorf("error dialing remote '%s': %s", mem.IP, err)
				}
				defer conn.Close()

				packetBytes, err := json.Marshal(message{
					Type:    msgTypePing,
					Port:    s.cfg.ReceiverPort, // Include your own port so the receiver knows who to talk back to
					Members: mems,
					UUID:    s.cfg.Name,
				})
				if err != nil {
					// A fairly severe error if we can't marshal our own messages
					return fmt.Errorf("error marshalling ping message: %s", err)
				}

				// Send the damn thing
				// TODO: check that we wrote all the bytes
				if _, err := fmt.Fprintf(conn, string(packetBytes)); err != nil {
					// It's udp, so if we can't send, something's wrong
					return fmt.Errorf("error writing to remote: %s", err)
				}

				return nil
			}()
		}
	}

	return nil
}
