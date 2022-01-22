package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jdholdren/swim/swim"
	"github.com/oklog/run"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var n int
	ns := os.Getenv("NUM_NODES")
	if ns != "" {
		n, _ = strconv.Atoi(ns)
	}
	if n == 0 {
		n = 2
	}

	// Start two processes to ping each other
	var g run.Group

	swims := []*swim.Swim{}

	// Start ports in the 4000 block
	for i := 0; i < n; i++ {
		p := 4000 + i
		g.Add(func() error {
			s, err := swim.New(swim.Config{
				K:            3,
				MessageSize:  1024,
				PingInterval: 200,
				ReceiverPort: p,
				InitialList: []string{
					fmt.Sprintf("127.0.0.1:%d", p-1),
				},
			})
			if err != nil {
				return err
			}
			swims = append(swims, s)

			return s.Listen(ctx)
		}, func(err error) {
			cancel()
		})
	}

	// A process to find out when they converge
	g.Add(func() error {
		for {
			t := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
			}

			// Go through each and count their nodes
			c := []int{}
			for _, s := range swims {
				ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
				mems, _ := s.Members(ctx)
				c = append(c, len(mems))

				cancel()
			}

			fmt.Printf("\ncounts: %#v", c)
		}
	}, func(err error) {
		cancel()
	})

	if err := g.Run(); err != nil {
		fmt.Printf("\nerror running: %s\n", err)
		os.Exit(1)
	}
}
