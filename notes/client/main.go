package main

import (
	"fmt"
	"github.com/satori/go.uuid"
	"github.com/scottfrazer/maple"
	"os"
	"time"
)

func main() {
	os.Remove("testdb")
	uuid := uuid.NewV4()
	kernel := maple.NewKernel(maple.NewLogger().ToWriter(os.Stdout), "sqlite3", "testdb", 1, 1)
	m := make(map[string]time.Duration)
	m["A"] = time.Second * 1
	m["B"] = time.Second * 1
	kernel.RegisterBackend("testbackend", maple.NewTestBackend(time.Second*0, m))
	kernel.On()
	err := kernel.Submit("[1]A[2]\n[A]B[3,4]", "inputs", "options", "testbackend", uuid, time.Second)

	if err != nil {
		fmt.Printf("fatal: %s\n", err)
	}

	time.Sleep(time.Millisecond * 100)
	err = kernel.Abort(uuid, time.Second)

	if err != nil {
		fmt.Printf("fatal: %s\n", err)
	}

	wi, err := kernel.Wait(uuid, time.Second*60)

	if err != nil {
		fmt.Printf("Got error trying to run workflow: %s\n", err)
	}

	if wi.Status() != "Aborted" {
		fmt.Printf("Expecting workflow status to be 'Aborted', got %s\n", wi.Status())
	}

	if wi.Uuid() != uuid {
		fmt.Printf("Expecting workflow instance UUID to be %s (got %s)\n", uuid, wi.Uuid())
	}

}
