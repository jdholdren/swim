package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/jdholdren/swim/swim"
	"github.com/oklog/run"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var n int
	ns := os.Getenv("NUM_NODES")
	if ns == "" {
		n, _ = strconv.Atoi(ns)
	}
	if n == 0 {
		n = 2
	}

	// Start two processes to ping each other
	var g run.Group

	// Start ports in the 4000 block
	for p := 4000; p < n; p++ {
		g.Add(func() error {
			s, err := swim.NewSwim(swim.Config{
				Name:         fmt.Sprintf("node_%d", p),
				K:            1,
				MessageSize:  1024,
				PingInterval: 1000,
				ReceiverPort: p,
			})
			if err != nil {
				return err
			}
			return s.Listen(ctx)
		}, func(err error) {
			cancel()
		})
	}

	if err := g.Run(); err != nil {
		fmt.Printf("\nerror running: %s\n", err)
		os.Exit(1)
	}
}
