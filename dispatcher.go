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
	done    *chan WorkflowExecutionResult
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
	done   *chan WorkflowExecutionResult
	source WorkflowSources
	status *[]CallStatus
}

type WorkflowExecutionResult struct {
	id         uuid.UUID
	status     string
	callStatus *[]CallStatus
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
	var cmdDone = make(chan bool, 1)

	go func() {
		cmd.Run()
		cmdDone <- true
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
		case <-cmdDone:
			var status = "done"
			if cmd.ProcessState == nil {
				status = "no-create"
			} else if !cmd.ProcessState.Success() {
				status = "failed"
			}
			fmt.Printf("subprocess: finish with: %s\n", status)
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

func workflowWorker(context WorkflowContext, done chan<- WorkflowExecutionResult, wg *sync.WaitGroup) {

	fmt.Printf("workflow %s: start\n", context.id)

	var callStatus = make([]CallStatus, 20)
	var result = WorkflowExecutionResult{context.id, "NotStarted", &callStatus}

	defer func() {
		if context.done != nil {
			*context.done <- result
			close(*context.done)
		}
		done <- result
		wg.Done()
		fmt.Printf("workflow %s: end\n", context.id)
	}()

	var runningCalls = 0
	var exitSentinal = false
	var subprocess_done = make(chan string)
	var subprocess_abort = make(map[string]chan bool)
	var call_status = make(map[string]string)
	var calls = make(chan string, 20)
	var timeout = time.After(WorkflowMaxRunTime)

	var abortSubprocesses = func() {
		for _, v := range subprocess_abort {
			v <- true
		}
		result.status = "Aborted"
	}

	go func() {
		calls <- "A"
		calls <- "B"
		calls <- "C"
		calls <- "D"
		calls <- "EXIT"
		//close(calls)
	}()

	for {
		select {
		case <-timeout:
			fmt.Printf("workflow %s: done (timeout %s)\n", context.id, WorkflowMaxRunTime)
			abortSubprocesses()
			return
		case call := <-calls:
			fmt.Printf("workflow %s: launching call: %s\n", context.id, call)
			subprocess_abort[call] = make(chan bool, 1)
			if call == "EXIT" {
				exitSentinal = true
			} else {
				runningCalls++
				go subprocess(exec.Command("sleep", "2"), call, subprocess_done, subprocess_abort[call])
			}
		case status := <-subprocess_done:
			fmt.Printf("workflow %s: subprocess finished: %s\n", context.id, status)
			runningCalls--
			var split = strings.Split(status, ":")
			var call = split[0]
			var sts = split[1]
			call_status[call] = sts
			if runningCalls == 0 && exitSentinal == true {
				var m = make(map[string]int)
				for _, v := range call_status {
					m[v] += 1
				}
				var a = make([]string, len(m))
				var i = 0
				for k, v := range m {
					a[i] = fmt.Sprintf("%s=%d", k, v)
					i++
				}
				result.status = strings.Join(a, ",")
				return
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
	var done = make(chan WorkflowExecutionResult)
	var wg sync.WaitGroup
	fmt.Printf("dispatcher: enter\n")
	defer fmt.Printf("dispatcher: exit\n")

	var processDone = func(result WorkflowExecutionResult) {
		fmt.Printf("dispatcher: workflow %s finished: %s\n", result.id, result.status)
		<-DbSetStatusAsync(fmt.Sprintf("%s", result.id), result.status)
		workers--
	}

	var kill = func() {
		fmt.Println("dispatcher: abort signal received...")
		close(SubmissionChannel)
		wg.Wait()
		fmt.Println("dispatcher: aborted")
	}

	for {
		if workers < max {
			select {
			case wf := <-SubmissionChannel:
				fmt.Printf("dispatcher: starting worker [workers: %d used / %d max], [queued: %d]\n", workers, max, len(SubmissionChannel))
				workers++
				wg.Add(1)
				sts := make([]CallStatus, 5)
				ctx := WorkflowContext{wf.id, wf.done, wf.sources, &sts}
				<-DbSetStatusAsync(fmt.Sprintf("%s", wf.id), "Started")
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

func IsAlive() bool {
	return isDispatcherAlive
}

func RunWorkflow(wdl, inputs, options string, id uuid.UUID) WorkflowExecutionResult {
	done := make(chan WorkflowExecutionResult, 1)

	sources := WorkflowSources{
		strings.TrimSpace(wdl),
		strings.TrimSpace(inputs),
		strings.TrimSpace(options)}

	<-DbSetStatusAsync(fmt.Sprintf("%s", id), "NotStarted")

	submission := WorkflowSubmission{id, &done, sources}
	SubmissionChannel <- submission
	result := <-done

	wfStatus := <-DbGetStatusAsync(fmt.Sprintf("%s", id))
	fmt.Printf("--- Workflow Completed: %s (status %s)\n", id, wfStatus)
	return result
}

func AbortWorkflow(id uuid.UUID) {
	return
}

func main() {
	StartDispatcher(1000, 4000)
	StartDbDispatcher()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			RunWorkflow("wdl", "inputs", "options", uuid.NewV4())
			wg.Done()
		}()
		time.Sleep(time.Millisecond * 10)
	}
	wg.Wait()

	fmt.Println("Listening on :8000 ...")
	http.HandleFunc("/submit", HttpEndpoint)
	http.ListenAndServe(":8000", nil)
}
