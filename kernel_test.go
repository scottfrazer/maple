package maple

import (
	"bytes"
	"fmt"
	"github.com/satori/go.uuid"
	"io/ioutil"
	"os"
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

func TestRunWorkflow(t *testing.T) {
	var buf bytes.Buffer
	uuid := uuid.NewV4()
	kernel := NewKernel(NewLogger().ToWriter(&buf).ToWriter(os.Stdout), "sqlite3", tmpFile(t), 1, 1)
	m := make(map[string]time.Duration)
	m["A"] = time.Second * 5
	kernel.RegisterBackend("testbackend", NewTestBackend(time.Second*0, m))
	kernel.On()
	err := kernel.Submit("[1]A[2]\n[A]B[3,4]", "inputs", "options", "testbackend", uuid, time.Second)

	if err != nil {
		t.Fatalf("fatal: %s", err)
	}

	err = kernel.Abort(uuid, time.Second)

	if err != nil {
		t.Fatalf("fatal: %s", err)
	}

	wi, err := kernel.Wait(uuid, time.Second*60)
	fmt.Println(buf.String())

	if err != nil {
		t.Fatalf("Got error trying to run workflow: %s", err)
	}

	if wi.Status() != "Aborted" {
		t.Fatalf("Expecting workflow status to be 'Aborted', got %s", wi.Status())
	}

	if wi.Uuid() != uuid {
		t.Fatalf("Expecting workflow instance UUID to be %s (got %s)", uuid, wi.Uuid())
	}

}
