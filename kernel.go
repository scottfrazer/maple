package main

import (
	"errors"
	"fmt"
	"github.com/satori/go.uuid"
	"golang.org/x/net/context"
	"gopkg.in/alecthomas/kingpin.v2"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

type WorkflowDispatcher struct {
	isAlive            bool
	maxWorkers         int
	submitChannel      chan *WorkflowContext
	submitChannelMutex *sync.Mutex
	cancel             func()
	waitGroup          *sync.WaitGroup
	db                 *MapleDb
	workflowMaxRuntime time.Duration
	log                *Logger
}

type WorkflowIdentifier interface {
	dbKey() int64
	id() uuid.UUID
}

type WorkflowSources struct {
	wdl     string
	inputs  string
	options string
}

type JobContext struct {
	primaryKey int64
	node       *Node
	index      int
	attempt    int
	status     string
}

func (ctx *JobContext) String() string {
	return fmt.Sprintf("%s (%s)", ctx.node.name, ctx.status)
}

type WorkflowContext struct {
	uuid       uuid.UUID
	primaryKey int64
	done       chan *WorkflowContext
	source     *WorkflowSources
	status     string
	calls      []*JobContext
}

func (c WorkflowContext) id() uuid.UUID {
	return c.uuid
}

func (c WorkflowContext) dbKey() int64 {
	return c.primaryKey
}

func (s WorkflowSources) String() string {
	return fmt.Sprintf("<workflow %s>", s.wdl)
}

func SubmitHttpEndpoint(wd *WorkflowDispatcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		uuid := uuid.NewV4()
		ctx, err := wd.db.NewWorkflow(uuid, &sources, wd.log)
		if err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, fmt.Sprintf(`{"message": "could not persist worflow"}`, r))
			return
		}

		wd.log.Info("HTTP endpoint /submit/ received: %s\n", sources)
		defer func() {
			if r := recover(); r != nil {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				io.WriteString(w, fmt.Sprintf(`{"message": "/submit/ panic: %s"}`, r))
			}
		}()

		select {
		case wd.submitChannel <- ctx:
		case <-time.After(time.Millisecond * 500):
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusRequestTimeout)
			io.WriteString(w, `{"message": "timeout submitting workflow (500ms)"}`)
			return
		}
	}
}

func (wd *WorkflowDispatcher) runJob(wfCtx *WorkflowContext, cmd *exec.Cmd, callCtx *JobContext, done chan<- *JobContext, jobCtx context.Context) {
	var cmdDone = make(chan bool, 1)
	var log = wd.log.ForJob(wfCtx.uuid, callCtx)
	var isAborting = false
	log.Info("runJob: enter")
	defer log.Info("runJob: exit")
	subprocessCtx, subprocessCancel := context.WithCancel(jobCtx)

	wd.db.SetJobStatus(callCtx, "Started", log)

	go func() {
		select {
		case <-time.After(time.Second * 2):
		case <-subprocessCtx.Done():
		}
		//cmd.Run()
		cmdDone <- true
	}()

	/*var kill = func(status string) {
		err := cmd.Process.Kill()
		if err != nil {
			panic(err)
		}
	}*/

	for {
		select {
		case <-cmdDone:
			var status = "Done"
			/*if cmd.ProcessState == nil {
				status = "no-create"
			} else if !cmd.ProcessState.Success() {
				status = "failed"
			}*/
			log.Info("runJob: done (status %s)", status)
			wd.db.SetJobStatus(callCtx, status, log)
			done <- callCtx
			return
		case <-jobCtx.Done():
			if !isAborting {
				log.Info("runJob: abort")
				subprocessCancel()
				isAborting = true
			}
		}
	}
}

func (wd *WorkflowDispatcher) runWorkflow(wfCtx *WorkflowContext, workflowResultsChannel chan<- *WorkflowContext, ctx context.Context) {
	var log = wd.log.ForWorkflow(wfCtx.uuid)

	log.Info("runWorkflow: start")

	// TODO: push these two lines into SetWorkflowStatus()
	wd.db.SetWorkflowStatus(wfCtx, "Started", log)
	wfCtx.status = "Started"

	var jobDone = make(chan *JobContext)
	// TODO: get rid of jobAbort, set the cancellation function on the JobContext
	var jobAbort = make(map[*Node]func())
	var jobAbortMutex sync.Mutex
	var workflowDone = make(chan bool)
	var calls = make(chan *Node, 20)
	var isAborting = false
	var doneCalls = make(chan *JobContext)

	defer func() {
		wfCtx.done <- wfCtx
		close(wfCtx.done)
		close(doneCalls)
		workflowResultsChannel <- wfCtx
	}()

	var abortSubprocesses = func() {
		for _, jobCloseFunc := range jobAbort {
			jobCloseFunc()
		}
		wd.db.SetWorkflowStatus(wfCtx, "Aborted", log)
		wfCtx.status = "Aborted"
		isAborting = true
	}

	go func() {
		reader := strings.NewReader(wfCtx.source.wdl)
		graph := LoadGraph(reader)
		for _, root := range graph.Root() {
			calls <- root
		}

		for call := range doneCalls {
			wfCtx.calls = append(wfCtx.calls, call)

			jobAbortMutex.Lock()
			delete(jobAbort, call.node)
			jobAbortMutex.Unlock()

			if len(wfCtx.calls) == len(graph.nodes) || (isAborting && len(jobAbort) == 0) {
				workflowDone <- true
				return
			} else if !isAborting {
				for _, nextCall := range graph.Downstream(call.node) {
					calls <- nextCall
				}
			}
		}
	}()

	for {
		if isAborting {
			select {
			case <-workflowDone:
				log.Info("workflow: completed")
				if wfCtx.status != "Aborted" {
					wfCtx.status = "Done"
					wd.db.SetWorkflowStatus(wfCtx, "Done", log)
				}
				return
			case call := <-jobDone:
				log.Info("workflow: subprocess finished: %s", call.status)
				doneCalls <- call
			}
		} else {
			select {
			case call := <-calls:
				log.Info("workflow: launching call: %s", call)
				jobAbortMutex.Lock()
				jobCtx, cancel := context.WithCancel(context.Background())
				jobAbort[call] = cancel
				job, err := wd.db.NewJob(wfCtx, call, log)
				if err != nil {
					// TODO: don't panic!
					panic(fmt.Sprintf("Couldn't persist job: %s", err))
				}
				go wd.runJob(wfCtx, exec.Command("sleep", "2"), job, jobDone, jobCtx)
				jobAbortMutex.Unlock()
			case <-workflowDone:
				log.Info("workflow: completed")
				wfCtx.status = "Done"
				wd.db.SetWorkflowStatus(wfCtx, "Done", log)
				return
			case call := <-jobDone:
				log.Info("workflow: subprocess finished: %s", call.status)
				doneCalls <- call
			case <-ctx.Done():
				// this is for cancellations AND timeouts
				log.Info("workflow: aborting...")
				abortSubprocesses()

				jobAbortMutex.Lock()
				if len(jobAbort) == 0 {
					jobAbortMutex.Unlock()
					return
				}
				jobAbortMutex.Unlock()
			}
		}
	}
}

func (wd *WorkflowDispatcher) runDispatcher(ctx context.Context) {
	var workers = 0
	var isAborting = false
	var workflowDone = make(chan *WorkflowContext)
	var workflowAbort = make(map[string]func())
	var runningWorkflows = make(map[string]*WorkflowContext)
	var log = wd.log

	log.Info("dispatcher: enter")
	defer func() {
		wd.waitGroup.Done()
		log.Info("dispatcher: exit")
	}()

	var abort = func() {
		wd.submitChannelMutex.Lock()
		close(wd.submitChannel)
		wd.isAlive = false
		wd.submitChannelMutex.Unlock()

		isAborting = true
		for _, wfCancelFunc := range workflowAbort {
			wfCancelFunc()
		}
	}

	var processDone = func(result *WorkflowContext) {
		log.Info("dispatcher: workflow %s finished: %s", result.uuid, result.status)
		delete(workflowAbort, fmt.Sprintf("%s", result.uuid))
		delete(runningWorkflows, fmt.Sprintf("%s", result.uuid))
		workers--
	}

	for {
		if isAborting {
			if len(runningWorkflows) == 0 {
				return
			}
			select {
			case d := <-workflowDone:
				processDone(d)
			}
		} else if workers < wd.maxWorkers {
			select {
			case wfContext := <-wd.submitChannel:
				workers++
				runningWorkflows[fmt.Sprintf("%s", wfContext.uuid)] = wfContext
				workflowCtx, workflowCancel := context.WithTimeout(ctx, wd.workflowMaxRuntime)
				workflowAbort[fmt.Sprintf("%s", wfContext.uuid)] = workflowCancel
				log.Info("dispatcher: starting %s", wfContext.uuid)
				go wd.runWorkflow(wfContext, workflowDone, workflowCtx)
			case d := <-workflowDone:
				processDone(d)
			case <-ctx.Done():
				abort()
			}
		} else {
			select {
			case d := <-workflowDone:
				processDone(d)
			case <-ctx.Done():
				abort()
			}
		}
	}
}

func NewWorkflowDispatcher(workers int, buffer int, log *Logger, db *MapleDb) *WorkflowDispatcher {
	var waitGroup sync.WaitGroup
	var mutex sync.Mutex
	dispatcherCtx, dispatcherCancel := context.WithCancel(context.Background())

	mutex.Lock()
	defer mutex.Unlock()

	wd := &WorkflowDispatcher{
		true,
		workers,
		make(chan *WorkflowContext, buffer),
		&mutex,
		dispatcherCancel,
		&waitGroup,
		db,
		time.Second * 600,
		log}

	waitGroup.Add(1)
	go wd.runDispatcher(dispatcherCtx)
	return wd
}

func (wd *WorkflowDispatcher) Abort() {
	if !wd.isAlive {
		return
	}
	wd.cancel()
	wd.Wait()
}

func (wd *WorkflowDispatcher) Wait() {
	wd.waitGroup.Wait()
}

func (wd *WorkflowDispatcher) IsAlive() bool {
	wd.submitChannelMutex.Lock()
	defer wd.submitChannelMutex.Unlock()
	return wd.isAlive
}

func (wd *WorkflowDispatcher) SubmitWorkflow(wdl, inputs, options string, id uuid.UUID) (*WorkflowContext, error) {
	sources := WorkflowSources{strings.TrimSpace(wdl), strings.TrimSpace(inputs), strings.TrimSpace(options)}
	log := wd.log.ForWorkflow(id)
	ctx, err := wd.db.NewWorkflow(id, &sources, log)
	if err != nil {
		return nil, err
	}
	wd.SubmitExistingWorkflow(ctx)
	return ctx, nil
}

func (wd *WorkflowDispatcher) SubmitExistingWorkflow(ctx *WorkflowContext) error {
	wd.submitChannelMutex.Lock()
	defer wd.submitChannelMutex.Unlock()
	if wd.isAlive == true {
		wd.submitChannel <- ctx
	} else {
		return errors.New("workflow submission is closed")
	}
	return nil
}

func (wd *WorkflowDispatcher) AbortWorkflow(id uuid.UUID) {
	return
}

func SignalHandler(wd *WorkflowDispatcher) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func(wd *WorkflowDispatcher) {
		sig := <-sigs
		wd.log.Info("%s signal detected... aborting dispatcher", sig)
		wd.Abort()
		wd.log.Info("%s signal detected... aborted dispatcher", sig)
		os.Exit(130)
	}(wd)
}

type Kernel struct {
	wd  *WorkflowDispatcher
	log *Logger
	db  *MapleDb
}

func NewKernel(log *Logger, dbName string, dbConnection string, concurrentWorkflows int, submitQueueSize int) *Kernel {
	db := NewMapleDb(dbName, dbConnection, log)
	wd := NewWorkflowDispatcher(concurrentWorkflows, submitQueueSize, log, db)
	SignalHandler(wd)
	return &Kernel{wd, log, db}
}

func (kernel *Kernel) RunWorkflow(wdl, inputs, options string, id uuid.UUID) *WorkflowContext {
	ctx, err := kernel.wd.SubmitWorkflow(wdl, inputs, options, id)
	if err != nil {
		return nil
	}
	return <-ctx.done
}

func (kernel *Kernel) SubmitWorkflow(wdl, inputs, options string, id uuid.UUID) *WorkflowContext {
	return nil
}

func (kernel *Kernel) AbortWorkflow(uuid uuid.UUID) error {
	return nil
}

func (kernel *Kernel) ListWorkflows() []uuid.UUID {
	return nil
}

func (kernel *Kernel) Shutdown() {
}

func main() {

	var (
		app          = kingpin.New("myapp", "A workflow engine")
		queueSize    = app.Flag("queue-size", "Submission queue size").Default("1000").Int()
		concurrentWf = app.Flag("concurrent-workflows", "Number of workflows").Default("1000").Int()
		logPath      = app.Flag("log", "Path to write logs").Default("maple.log").String()
		restart      = app.Command("restart", "Restart workflows")
		run          = app.Command("run", "Run workflows")
		runGraph     = run.Arg("wdl", "Graph file").Required().String()
		runN         = run.Arg("count", "Number of instances").Required().Int()
		server       = app.Command("server", "Start HTTP server")
	)

	args, err := app.Parse(os.Args[1:])
	log := NewLogger().ToFile(*logPath).ToWriter(os.Stdout)
	engine := NewKernel(log, "sqlite3", "DB", *concurrentWf, *queueSize)

	switch kingpin.MustParse(args, err) {
	case restart.FullCommand():
		restartableWorkflows, _ := engine.db.GetWorkflowsByStatus(log, "Aborted", "NotStarted", "Started")
		var restartWg sync.WaitGroup
		for _, restartableWfContext := range restartableWorkflows {
			fmt.Printf("restarting %s\n", restartableWfContext.uuid)
			restartWg.Add(1)
			go func(ctx *WorkflowContext) {
				engine.wd.SubmitExistingWorkflow(ctx)
				<-ctx.done
				restartWg.Done()
			}(restartableWfContext)
		}
		restartWg.Wait()
	case run.FullCommand():
		var wg sync.WaitGroup
		for i := 0; i < *runN; i++ {
			wg.Add(1)
			go func() {
				contents, err := ioutil.ReadFile(*runGraph)
				if err != nil {
					// TODO: don't panic
					panic(err)
				}

				id := uuid.NewV4()
				ctx := engine.RunWorkflow(string(contents), "inputs", "options", id)
				if ctx != nil {
					engine.log.Info("Workflow Complete: %s (status %s)", id, ctx.status)
				} else {
					engine.log.Info("Workflow Incomplete")
				}
				wg.Done()
			}()
		}
		wg.Wait()
	case server.FullCommand():
		log.Info("Listening on :8000 ...")
		http.HandleFunc("/submit", SubmitHttpEndpoint(engine.wd))
		http.ListenAndServe(":8000", nil)
	}

	engine.wd.Abort()
}
