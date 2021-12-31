package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/jdholdren/swim/swim"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	s := &swim.Swim{}

	if err := s.Listen(ctx); err != nil {
		log.Fatalf("error listening: %s", err)
	}
}
