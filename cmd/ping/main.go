package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/oklog/run"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	addr1 := net.UDPAddr{
		Port: 4445,
		IP:   net.ParseIP("127.0.0.1"),
	}
	addr2 := net.UDPAddr{
		Port: 4444,
		IP:   net.ParseIP("127.0.0.1"),
	}

	logger, _ := zap.NewDevelopment()
	logger1 := logger.Sugar().With("proc", 1)
	logger2 := logger.Sugar().With("proc", 2)

	// Start two processes to ping each other
	var g run.Group

	g.Add(func() error {
		return nodeProc(ctx, logger1, addr1, addr2)
	}, func(err error) {
		logger2.Infof("error with process 1: %s", err)
		cancel()
	})
	g.Add(func() error {
		return nodeProc(ctx, logger2, addr2, addr1)
	}, func(err error) {
		logger2.Infof("error with process 2: %s", err)
		cancel()
	})

	if err := g.Run(); err != nil {
		fmt.Printf("\nerror running: %s\n", err)
		os.Exit(1)
	}
}

type message struct {
	remote *net.UDPAddr
	byts   []byte
	length int
}

func nodeProc(ctx context.Context, l *zap.SugaredLogger, addr, remote net.UDPAddr) error {
	conn, err := net.ListenUDP("udp", &addr)
	if err != nil {
		return fmt.Errorf("could not open connection: %s", err)
	}
	defer conn.Close()

	g := new(errgroup.Group)
	// Receiver listener
	g.Go(func() error {
		rcv := make(chan message, 10)
		errs := make(chan error)

		// The read loop is blocking
		go func() {
			for {
				b := make([]byte, 1024)
				// Receive a message
				l, remoteAddr, err := conn.ReadFromUDP(b)
				if err != nil {
					errs <- fmt.Errorf("error reading from remote: %s", err)
				}

				// Log out the message
				c := make([]byte, 1024)
				copy(c, b)
				rcv <- message{
					remote: remoteAddr,
					byts:   c,
					length: l,
				}
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return errors.New("interrupt")
			case msg := <-rcv:
				go func(msg message) {
					// Shorten the message
					l.Infow("message received", "sender_port", msg.remote.Port, "msg", string(msg.byts[:msg.length]))
				}(msg)
			case err := <-errs:
				return err
			}
		}

		return nil
	})

	// The PINGER loop
	g.Go(func() error {
		var count int
		for {
			// Set a random timeout for a timer
			i := rand.Intn(10)
			t := time.NewTimer(time.Second * time.Duration(i))

			// Wait for cancellation to exit our loop or the timer to execute the ping
			select {
			case <-ctx.Done():
				return errors.New("interrupt")
			case <-t.C:
				t.Stop()
			}

			// Write a message to the socket
			conn, err := net.Dial("udp", fmt.Sprintf("%s:%d", remote.IP.String(), remote.Port))
			if err != nil {
				return fmt.Errorf("error dialing remote: %s", err)
			}
			defer conn.Close()

			if _, err := fmt.Fprintf(conn, "Hi UDP Server, How are you doing?"); err != nil {
				return fmt.Errorf("error dialing remote: %s", err)
			}

			l.Info("sent")

			count += 1
		}

		return nil
	})

	if err := g.Wait(); err != nil {
		return fmt.Errorf("error from group: %s", err)
	}

	return nil
}
