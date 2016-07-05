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
	id      uuid.UUID
	wdl     string
	inputs  string
	options string
}

func (s WorkflowSources) String() string {
	return fmt.Sprintf("<workflow %s>", s.id)
}

var WorkflowChannel chan WorkflowSources
var WorkflowMaxRunTime = time.Second * 2
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

	sources := WorkflowSources{
		uuid.NewV4(),
		strings.TrimSpace(wdl),
		strings.TrimSpace(inputs),
		strings.TrimSpace(options)}

	fmt.Printf("HTTP endpoint /submit/ received: %s\n", sources)
	defer func() {
		if r := recover(); r != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, fmt.Sprintf(`{"message": "/submit/ panic: %s"}`, r))
		}
	}()

	select {
	case WorkflowChannel <- sources:
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

func workflowWorker(sources WorkflowSources, done chan<- string, wg *sync.WaitGroup) {
	defer wg.Done()
	fmt.Printf("workflow start\n")
	defer fmt.Printf("workflow end\n")

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
		done <- fmt.Sprintf("aborted:%s", sources.id)
	}

	go func() {
		calls <- "A"
		time.Sleep(time.Second * 3)
		calls <- "B"
		time.Sleep(time.Second * 3)
		calls <- "C"
		time.Sleep(time.Second * 12)
		calls <- "D"
		//close(calls)
	}()

	for !workflow_terminal {
		select {
		case <-timeout:
			fmt.Printf("workflow %s: done (timeout %s)\n", sources.id, WorkflowMaxRunTime)
			abortSubprocesses()
			return
		case call := <-calls:
			fmt.Printf("workflow %s: launching call: %s\n", sources.id, call)
			subprocess_abort[call] = make(chan bool, 1)
			go subprocess(exec.Command("sleep", "2"), call, subprocess_done, subprocess_abort[call])
		case status := <-subprocess_done:
			fmt.Printf("workflow %s: subprocess finished: %s\n", sources.id, status)
			var split = strings.Split(status, ":")
			var call = split[0]
			var sts = split[1]
			call_status[call] = sts
			fmt.Printf("workflow %s: len(call_status) = %d\n", sources.id, len(call_status))
			if len(call_status) == 4 {
				fmt.Printf("workflow %s: terminal workflow\n", sources.id)
				var status = "done"
				for _, v := range call_status {
					if v != "done" {
						status = "failed"
					}
				}
				done <- fmt.Sprintf("%s:%s", status, sources.id)
				workflow_terminal = true
			}
		case _, ok := <-dispatcherAbortChannel:
			if !ok {
				fmt.Printf("workflow %s: aborting...\n", sources.id)
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
		close(WorkflowChannel)
		wg.Wait()
		fmt.Println("dispatcher: aborted")
	}

	for {
		fmt.Printf("dispatcher: [workers: %d used / %d max], [queue %d used  / %s max]\n", workers, max, len(WorkflowChannel), "?")
		if workers < max {
			select {
			case wf := <-WorkflowChannel:
				workers++
				wg.Add(1)
				go workflowWorker(wf, done, &wg)
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
		WorkflowChannel = make(chan WorkflowSources, buffer)
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

func bug() {
	for {
		if isAlive() {
			select {
			case <-time.After(time.Second * 20):
				fmt.Println("\n\n!!!!!!!!!!! 20 seconds is up ... aborting dispatcher !!!!!!!!!!!!!!!\n\n")
				AbortDispatcher()
			}
		} else {
			select {
			case <-time.After(time.Second * 1):
				fmt.Println("\n\n!!!!!!!!!!! 1 second later ... create dispatcher !!!!!!!!!!!!!!!\n\n")
				StartDispatcher(5, 20)
			}
		}
	}
}

func main() {
	StartDispatcher(20, 20)
	http.HandleFunc("/submit", HttpEndpoint)
	//go bug()
	http.ListenAndServe(":8000", nil)
}
