package main

import (
	//"fmt"
	"github.com/satori/go.uuid"
	"testing"
	//"time"
)

func TestStartDispatcher(t *testing.T) {
	wd := NewDispatcher(1, 1)
	if !wd.IsAlive() {
		t.Fatalf("Expecting the dispatcher to be alive after starting it")
	}
}

func TestRunWorkflow(t *testing.T) {
	StartDbDispatcher()
	wd := NewDispatcher(1, 1)
	context := wd.RunWorkflow("wdl", "inputs", "options", uuid.NewV4())
	if context.status != "Done" {
		t.Fatalf("Expecting workflow status to be 'Done'")
	}
}
