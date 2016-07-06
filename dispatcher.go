package main

import (
	"fmt"
	"github.com/satori/go.uuid"
	"io"
	"io/ioutil"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type WorkflowSources struct {
	wdl     string
	inputs  string
	options string
}

type WorkflowSubmission struct {
	id      uuid.UUID
	done    *chan bool
	sources WorkflowSources
}

type CallStatus struct {
	call    string
	index   int
	attempt int
	status  string
}

type WorkflowContext struct {
	id     uuid.UUID
	done   *chan bool
	source WorkflowSources
	status *[]CallStatus
}

func (s WorkflowSources) String() string {
	return fmt.Sprintf("<workflow %s>", s.wdl)
}

var SubmissionChannel chan WorkflowSubmission
var WorkflowMaxRunTime = time.Second * 30
var dispatcherAbortChannel chan bool
var isDispatcherAlive = false

func HttpEndpoint(w http.ResponseWriter, r *http.Request) {
	fp, _, err := r.FormFile("wdl")
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"message": "no WDL file"}`)
		return
	}

	var bytes, _ = ioutil.ReadAll(fp)
	wdl := string(bytes)

	fp, _, err = r.FormFile("inputs")
	var inputs = "{}"
	if err != nil {
		bytes, _ = ioutil.ReadAll(fp)
		inputs = string(bytes)
	}

	fp, _, err = r.FormFile("options")
	var options = "{}"
	if err != nil {
		bytes, _ = ioutil.ReadAll(fp)
		options = string(bytes)
	}

	sources := WorkflowSources{strings.TrimSpace(wdl), strings.TrimSpace(inputs), strings.TrimSpace(options)}
	submission := WorkflowSubmission{uuid.NewV4(), nil, sources}

	fmt.Printf("HTTP endpoint /submit/ received: %s\n", sources)
	defer func() {
		if r := recover(); r != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, fmt.Sprintf(`{"message": "/submit/ panic: %s"}`, r))
		}
	}()

	select {
	case SubmissionChannel <- submission:
	case <-time.After(time.Millisecond * 500):
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusRequestTimeout)
		io.WriteString(w, `{"message": "timeout submitting workflow (500ms)"}`)
		return
	}
}

func subprocess(cmd *exec.Cmd, name string, done chan<- string, subprocess_abort <-chan bool) {
	fmt.Printf("subprocess: start %s\n", cmd.Path)
	defer fmt.Printf("subprocess: end %s\n", cmd.Path)
	var proc_done_channel = make(chan bool, 1)

	go func() {
		cmd.Run()
		proc_done_channel <- true
	}()

	var kill = func(status string) {
		err := cmd.Process.Kill()
		done <- fmt.Sprintf("%s:%s", name, status)
		if err != nil {
			panic(err)
		}
	}

	for {
		select {
		case <-proc_done_channel:
			var status = "done"
			if !cmd.ProcessState.Success() {
				status = "failed"
			}
			fmt.Printf("subprocess: process %s: %d\n", status, cmd.Process.Pid)
			done <- fmt.Sprintf("%s:%s", name, status)
			return
		case <-subprocess_abort:
			fmt.Printf("subprocess: abort PID %d\n", cmd.Process.Pid)
			kill("aborted")
			return
		case _, ok := <-dispatcherAbortChannel:
			if !ok {
				fmt.Printf("subprocess: aborting...\n")
				kill("aborted")
				return
			}
		}
	}
}

func workflowWorker(context WorkflowContext, done chan<- string, wg *sync.WaitGroup) {
	defer func() {
		if context.done != nil {
			*context.done <- true
			close(*context.done)
		}
		wg.Done()
	}()

	fmt.Printf("workflow %s: start\n", context.id)
	defer fmt.Printf("workflow %s: end\n", context.id)

	var subprocess_done = make(chan string)
	var subprocess_abort = make(map[string]chan bool)
	var call_status = make(map[string]string)
	var calls = make(chan string)
	var workflow_terminal = false
	var timeout = time.After(WorkflowMaxRunTime)

	var abortSubprocesses = func() {
		for _, v := range subprocess_abort {
			v <- true
		}
		workflow_terminal = true
		done <- fmt.Sprintf("aborted:%s", context.id)
	}

	go func() {
		calls <- "A"
		time.Sleep(time.Second * 3)
		calls <- "B"
		time.Sleep(time.Second * 3)
		calls <- "C"
		time.Sleep(time.Second * 3)
		calls <- "D"
		//close(calls)
	}()

	for !workflow_terminal {
		select {
		case <-timeout:
			fmt.Printf("workflow %s: done (timeout %s)\n", context.id, WorkflowMaxRunTime)
			abortSubprocesses()
			return
		case call := <-calls:
			fmt.Printf("workflow %s: launching call: %s\n", context.id, call)
			subprocess_abort[call] = make(chan bool, 1)
			go subprocess(exec.Command("sleep", "2"), call, subprocess_done, subprocess_abort[call])
		case status := <-subprocess_done:
			fmt.Printf("workflow %s: subprocess finished: %s\n", context.id, status)
			var split = strings.Split(status, ":")
			var call = split[0]
			var sts = split[1]
			call_status[call] = sts
			fmt.Printf("workflow %s: len(call_status) = %d\n", context.id, len(call_status))
			if len(call_status) == 4 {
				fmt.Printf("workflow %s: terminal workflow\n", context.id)
				var status = "done"
				for _, v := range call_status {
					if v != "done" {
						status = "failed"
					}
				}
				done <- fmt.Sprintf("%s:%s", status, context.id)
				workflow_terminal = true
			}
		case _, ok := <-dispatcherAbortChannel:
			if !ok {
				fmt.Printf("workflow %s: aborting...\n", context.id)
				abortSubprocesses()
				return
			}
		}
	}
}

func workflowDispatcher(max int) {
	var workers = 0
	var done = make(chan string)
	var wg sync.WaitGroup
	fmt.Printf("dispatcher: enter\n")
	defer fmt.Printf("dispatcher: exit\n")

	var processDone = func(msg string) {
		fmt.Printf("dispatcher: workflow finished: %s\n", msg)
		workers--
	}

	var kill = func() {
		fmt.Println("dispatcher: abort signal received...")
		close(SubmissionChannel)
		wg.Wait()
		fmt.Println("dispatcher: aborted")
	}

	for {
		fmt.Printf("dispatcher: [workers: %d used / %d max], [queued: %d]\n", workers, max, len(SubmissionChannel))
		if workers < max {
			select {
			case wf := <-SubmissionChannel:
				workers++
				wg.Add(1)
				sts := make([]CallStatus, 5)
				ctx := WorkflowContext{wf.id, wf.done, wf.sources, &sts}
				go workflowWorker(ctx, done, &wg)
			case d := <-done:
				processDone(d)
			case _, ok := <-dispatcherAbortChannel:
				if !ok {
					kill()
					return
				}
			}
		} else {
			select {
			case d := <-done:
				processDone(d)
			case _, ok := <-dispatcherAbortChannel:
				if !ok {
					kill()
					return
				}
			}
		}
	}
}

func StartDispatcher(workers int, buffer int) {
	if !isDispatcherAlive {
		SubmissionChannel = make(chan WorkflowSubmission, buffer)
		dispatcherAbortChannel = make(chan bool)
		go workflowDispatcher(workers)
		isDispatcherAlive = true
	} else {
		fmt.Println("Dispatcher already started")
	}
}

func AbortDispatcher() {
	close(dispatcherAbortChannel)
	isDispatcherAlive = false
}

func isAlive() bool {
	return isDispatcherAlive
}

// Main API here

func StartWorkflow(wdl, inputs, options string) (chan bool, uuid.UUID) {
	id := uuid.NewV4()
	done := make(chan bool, 1)

	sources := WorkflowSources{
		strings.TrimSpace(wdl),
		strings.TrimSpace(inputs),
		strings.TrimSpace(options)}

	submission := WorkflowSubmission{id, &done, sources}

	SubmissionChannel <- submission

	return done, id
}

func main() {
	StartDispatcher(1000, 10000)
	for i := 0; i < 4000; i++ {
		StartWorkflow("wdl", "inputs", "options")
	}
	//http.HandleFunc("/submit", HttpEndpoint)
	//http.ListenAndServe(":8000", nil)
}
