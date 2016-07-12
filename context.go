package main

import (
	"fmt"
	"golang.org/x/net/context"
	"time"
)

func f(id string, ctx context.Context) {
	childCtx, cancel := context.WithTimeout(ctx, time.Second*2)
	defer func() {
		cancel()
		fmt.Printf("f(%s) exit\n", id)
	}()

	if id == "A" {
		go f("B", childCtx)
	}

	select {
	case <-ctx.Done():
		fmt.Printf("f(%s) cancel: %s\n", id, ctx.Err())
	case <-time.After(time.Second * 5):
		fmt.Printf("f(%s) timeout\n", id)
	}
}

func main2() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f("A", ctx)

	fmt.Println(ctx, cancel)
	time.Sleep(time.Second * 15)
}
