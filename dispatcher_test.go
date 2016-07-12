package main

import (
	//"fmt"
	"github.com/satori/go.uuid"
	"testing"
	//"time"
)

func TestStartDispatcher(t *testing.T) {
	if IsAlive() {
		t.Fatalf("Expecting the dispatcher to start as not being alive")
	}
	StartDispatcher(1, 1)
	if IsAlive() {
		t.Fatalf("Expecting the dispatcher to be alive after starting it")
	}
}

func TestRunWorkflow(t *testing.T) {
	StartDbDispatcher()
	StartDispatcher(1, 1)
	context := RunWorkflow("wdl", "inputs", "options", uuid.NewV4())
	if context.status != "Done" {
		t.Fatalf("Expecting workflow status to be 'Done'")
	}
}
