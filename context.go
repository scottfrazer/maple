package main

import (
	"fmt"
	"golang.org/x/net/context"
	"sync"
	"time"
)

var c = make(chan int)

func g(id string, ctx context.Context, wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
		fmt.Printf("%s exit\n", id)
	}()

	select {
	case <-ctx.Done():
		fmt.Printf("%s cancel: %s\n", id, ctx.Err())
	case <-c:
		fmt.Printf("shouldn't happen")
	}
}

func f(id string, ctx context.Context, wg *sync.WaitGroup) {
	//childCtx, cancel := context.WithTimeout(ctx, time.Second*2)
	childCtx, cancel := context.WithCancel(ctx)
	var childWg sync.WaitGroup

	defer func() {
		cancel()
		childWg.Wait()
		wg.Done()
		fmt.Printf("%s exit\n", id)
	}()

	for i := 0; i < 2; i++ {
		childWg.Add(1)
		go g(fmt.Sprintf("%s-g%d", id, i), childCtx, &childWg)
	}

	select {
	case <-ctx.Done():
		fmt.Printf("%s cancel: %s\n", id, ctx.Err())
	case <-c:
		fmt.Printf("shouldn't happen")
	}
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go f(fmt.Sprintf("f%d", i), ctx, &wg)
	}

	time.Sleep(time.Second * 5)
	fmt.Println("cancel()")
	cancel()
	wg.Wait()
}
