package maple

import (
	"bytes"
	"fmt"
	"github.com/satori/go.uuid"
	"io/ioutil"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

var tmpFiles []string
var tmpFilesMtx sync.Mutex

func tmpFile(t *testing.T) string {
	tmpFilesMtx.Lock()
	defer tmpFilesMtx.Unlock()
	file, err := ioutil.TempFile(os.TempDir(), "mapleDB")
	if err != nil {
		t.Fatalf("Could not create temp file")
	}
	tmpFiles = append(tmpFiles, file.Name())
	return file.Name()
}

func TestMain(m *testing.M) {
	rc := m.Run()
	for _, file := range tmpFiles {
		os.Remove(file)
	}
	os.Exit(rc)
}

func makeTestKernel(t *testing.T) (*bytes.Buffer, *TestBackend, *Kernel) {
	var buf bytes.Buffer
	kernel := NewKernel(NewLogger().ToWriter(&buf), "sqlite3", tmpFile(t), 1, 1)
	backend := NewTestBackend(time.Second * 0)
	kernel.RegisterBackend("testbackend", backend)
	kernel.On()
	return &buf, &backend, kernel
}

func TestRunWorkflow(t *testing.T) {
	logs, _, kernel := makeTestKernel(t)
	uuid := uuid.NewV4()
	wi, err := kernel.Run("[1]A[2]\n[A]B[3,4]", "inputs", "options", "testbackend", uuid)

	if err != nil {
		t.Fatalf("fatal: %s", err)
	}

	if wi.Status() != "Done" {
		t.Fatalf("Expecting workflow status to be 'Done', got %s", wi.Status())
	}

	if wi.Uuid() != uuid {
		t.Fatalf("Expecting workflow instance UUID to be %s (got %s)", uuid, wi.Uuid())
	}

	assertMessage := func(msg string) {
		if !strings.Contains(logs.String(), msg) {
			t.Fatalf("Expecting to see log message: %s", msg)
		}
	}

	messages := [...]string{
		fmt.Sprintf("[%s:A] status change Started -> Done", uuid.String()[:8]),
		fmt.Sprintf("[%s:B] status change Started -> Done", uuid.String()[:8]),
		fmt.Sprintf("workflow %s finished: Done", uuid)}

	for _, message := range messages {
		assertMessage(message)
	}
}

func TestSubmitAndWaitForWorkflow(t *testing.T) {
	logs, _, kernel := makeTestKernel(t)
	uuid := uuid.NewV4()
	err := kernel.Submit("[1]A[2]\n[A]B[3,4]", "inputs", "options", "testbackend", uuid, time.Second)

	if err != nil {
		t.Fatalf("fatal: %s", err)
	}

	kernel.Wait(uuid, time.Second*60)
	wi := kernel.SnapshotOf(uuid)

	if wi.Status() != "Done" {
		t.Fatalf("Expecting workflow status to be 'Done', got %s", wi.Status())
	}

	if wi.Uuid() != uuid {
		t.Fatalf("Expecting workflow instance UUID to be %s (got %s)", uuid, wi.Uuid())
	}

	assertMessage := func(msg string) {
		if !strings.Contains(logs.String(), msg) {
			t.Fatalf("Expecting to see log message: %s", msg)
		}
	}

	// TODO: only use .Trace() messages for asserting on!
	messages := [...]string{
		fmt.Sprintf("[%s:A] status change Started -> Done", uuid.String()[:8]),
		fmt.Sprintf("[%s:B] status change Started -> Done", uuid.String()[:8]),
		fmt.Sprintf("workflow %s finished: Done", uuid)}

	for _, message := range messages {
		assertMessage(message)
	}
}

func TestAbortWorkflowStepOne(t *testing.T) {
	logs, backend, kernel := makeTestKernel(t)
	backend.SetRuntime("A", time.Second*5)

	uuid := uuid.NewV4()
	err := kernel.Submit("[1]A[2]\n[A]B[3,4]", "inputs", "options", "testbackend", uuid, time.Second)

	if err != nil {
		t.Fatalf("fatal: %s", err)
	}

	err = kernel.Abort(uuid, time.Second)

	if err != nil {
		t.Fatalf("fatal: %s", err)
	}

	kernel.Wait(uuid, time.Second*60)
	wi := kernel.SnapshotOf(uuid)

	if wi.Status() != "Aborted" {
		t.Fatalf("Expecting workflow status to be 'Aborted', got %s", wi.Status())
	}

	if wi.Uuid() != uuid {
		t.Fatalf("Expecting workflow instance UUID to be %s (got %s)", uuid, wi.Uuid())
	}

	abortMessage := fmt.Sprintf("workflow %s finished: Aborted", uuid)
	if !strings.Contains(logs.String(), abortMessage) {
		t.Fatalf("Expecting to see log message: %s", abortMessage)
	}
}

func TestAbortWorkflowStepTwo(t *testing.T) {
	logs, backend, kernel := makeTestKernel(t)
	backend.SetRuntime("B", time.Second*5)

	uuid := uuid.NewV4()
	err := kernel.Submit("[1]A[2]\n[A]B[3,4]", "inputs", "options", "testbackend", uuid, time.Second)

	if err != nil {
		t.Fatalf("fatal: %s", err)
	}

	time.Sleep(time.Millisecond * 500)
	err = kernel.Abort(uuid, time.Second)

	if err != nil {
		t.Fatalf("fatal: %s", err)
	}

	kernel.Wait(uuid, time.Second*60)
	wi := kernel.SnapshotOf(uuid)

	if wi.Status() != "Aborted" {
		t.Fatalf("Expecting workflow status to be 'Aborted', got %s", wi.Status())
	}

	if wi.Uuid() != uuid {
		t.Fatalf("Expecting workflow instance UUID to be %s (got %s)", uuid, wi.Uuid())
	}

	assertMessage := func(msg string) {
		if !strings.Contains(logs.String(), msg) {
			t.Fatalf("Expecting to see log message: %s", msg)
		}
	}

	// TODO: only use .Trace() messages for asserting on!
	messages := [...]string{
		fmt.Sprintf("[%s:A] status change NotStarted -> Started", uuid.String()[:8]),
		fmt.Sprintf("[%s:A] status change Started -> Done", uuid.String()[:8]),
		fmt.Sprintf("[%s:B] status change NotStarted -> Started", uuid.String()[:8]),
		fmt.Sprintf("[%s:B] status change Started -> Aborted", uuid.String()[:8]),
		fmt.Sprintf("workflow %s finished: Aborted", uuid)}

	for _, message := range messages {
		assertMessage(message)
	}
}
