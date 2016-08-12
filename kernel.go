package main

import (
	"errors"
	"fmt"
	"github.com/satori/go.uuid"
	"golang.org/x/net/context"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

type workflowDispatcher struct {
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
	cancel     func()
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

func (s WorkflowSources) String() string {
	return fmt.Sprintf("<workflow %s>", s.wdl)
}

func (wd *workflowDispatcher) runJob(wfCtx *WorkflowContext, cmd *exec.Cmd, callCtx *JobContext, done chan<- *JobContext, jobCtx context.Context) {
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

func (wd *workflowDispatcher) runWorkflow(wfCtx *WorkflowContext, workflowResultsChannel chan<- *WorkflowContext, ctx context.Context) {
	var log = wd.log.ForWorkflow(wfCtx.uuid)

	log.Info("runWorkflow: start")

	wd.db.SetWorkflowStatus(wfCtx, "Started", log)

	var jobs = make(map[*JobContext]struct{})
	var jobMutex sync.Mutex
	var jobDone = make(chan *JobContext)
	var workflowDone = make(chan bool)
	var runnableJobs = make(chan *JobContext)
	var isAborting = false
	var doneJobs = make(chan *JobContext)

	defer func() {
		wfCtx.done <- wfCtx
		close(wfCtx.done)
		close(doneJobs)
		workflowResultsChannel <- wfCtx
	}()

	var abortJobs = func() {
		for job, _ := range jobs {
			job.cancel()
		}
		wd.db.SetWorkflowStatus(wfCtx, "Aborted", log)
		isAborting = true
	}

	go func() {
		reader := strings.NewReader(wfCtx.source.wdl)
		graph := LoadGraph(reader)
		for _, root := range graph.Root() {
			// TODO: duplicated code from below
			job, err := wd.db.NewJob(wfCtx, root, log)
			if err != nil {
				// TODO: don't panic
				panic(err)
			}
			runnableJobs <- job
		}

		for doneJob := range doneJobs {
			wfCtx.calls = append(wfCtx.calls, doneJob)

			jobMutex.Lock()
			delete(jobs, doneJob)
			jobMutex.Unlock()

			if len(wfCtx.calls) == len(graph.nodes) || (isAborting && len(jobs) == 0) {
				workflowDone <- true
				return
			} else if !isAborting {
				go func() {
					for _, nextNode := range graph.Downstream(doneJob.node) {
						// TODO: check if this job already exists in DB.
						// If so, then don't create a new one in the DB
						job, err := wd.db.NewJob(wfCtx, nextNode, log)
						if err != nil {
							// TODO: don't panic
							panic(err)
						}
						runnableJobs <- job
					}
				}()
			}
		}
	}()

	for {
		if isAborting {
			select {
			case <-workflowDone:
				log.Info("workflow: completed")
				if wfCtx.status != "Aborted" {
					wd.db.SetWorkflowStatus(wfCtx, "Done", log)
				}
				return
			case call := <-jobDone:
				log.Info("workflow: subprocess finished: %s", call.status)
				doneJobs <- call
			}
		} else {
			select {
			case job := <-runnableJobs:
				log.Info("workflow: launching call: %s", job.node.String())
				jobMutex.Lock()
				jobCtx, cancel := context.WithCancel(context.Background())
				job.cancel = cancel
				jobs[job] = struct{}{}
				go wd.runJob(wfCtx, exec.Command("sleep", "2"), job, jobDone, jobCtx)
				jobMutex.Unlock()
			case <-workflowDone:
				log.Info("workflow: completed")
				wd.db.SetWorkflowStatus(wfCtx, "Done", log)
				return
			case call := <-jobDone:
				log.Info("workflow: subprocess finished: %s", call.status)
				doneJobs <- call
			case <-ctx.Done():
				// this is for cancellations AND timeouts
				log.Info("workflow: aborting...")
				abortJobs()

				jobMutex.Lock()
				if len(jobs) == 0 {
					jobMutex.Unlock()
					return
				}
				jobMutex.Unlock()
			}
		}
	}
}

func (wd *workflowDispatcher) runDispatcher(ctx context.Context) {
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

func newWorkflowDispatcher(workers int, buffer int, log *Logger, db *MapleDb) *workflowDispatcher {
	var waitGroup sync.WaitGroup
	var mutex sync.Mutex
	dispatcherCtx, dispatcherCancel := context.WithCancel(context.Background())

	mutex.Lock()
	defer mutex.Unlock()

	wd := &workflowDispatcher{
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

func (wd *workflowDispatcher) abort() {
	if !wd.isAlive {
		return
	}
	wd.cancel()
	wd.wait()
}

func (wd *workflowDispatcher) wait() {
	wd.waitGroup.Wait()
}

func (wd *workflowDispatcher) SubmitWorkflow(wdl, inputs, options string, id uuid.UUID, timeout time.Duration) (*WorkflowContext, error) {
	sources := WorkflowSources{strings.TrimSpace(wdl), strings.TrimSpace(inputs), strings.TrimSpace(options)}
	log := wd.log.ForWorkflow(id)
	ctx, err := wd.db.NewWorkflow(id, &sources, log)
	if err != nil {
		return nil, err
	}
	wd.SubmitExistingWorkflow(ctx, timeout)
	return ctx, nil
}

func (wd *workflowDispatcher) SubmitExistingWorkflow(ctx *WorkflowContext, timeout time.Duration) error {
	wd.submitChannelMutex.Lock()
	defer wd.submitChannelMutex.Unlock()
	if wd.isAlive == true {
		select {
		case wd.submitChannel <- ctx:
		case <-time.After(timeout):
			return errors.New("Timeout submitting workflow")
		}
	} else {
		return errors.New("workflow submission is closed")
	}
	return nil
}

func (wd *workflowDispatcher) AbortWorkflow(id uuid.UUID) {
	return
}

func SignalHandler(wd *workflowDispatcher) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func(wd *workflowDispatcher) {
		sig := <-sigs
		wd.log.Info("%s signal detected... aborting dispatcher", sig)
		wd.abort()
		wd.log.Info("%s signal detected... aborted dispatcher", sig)
		os.Exit(130)
	}(wd)
}

type Kernel struct {
	wd  *workflowDispatcher
	log *Logger
	db  *MapleDb
}

func NewKernel(log *Logger, dbName string, dbConnection string, concurrentWorkflows int, submitQueueSize int) *Kernel {
	db := NewMapleDb(dbName, dbConnection, log)
	wd := newWorkflowDispatcher(concurrentWorkflows, submitQueueSize, log, db)
	SignalHandler(wd)
	return &Kernel{wd, log, db}
}

func (kernel *Kernel) RunWorkflow(wdl, inputs, options string, id uuid.UUID) (*WorkflowContext, error) {
	ctx, err := kernel.wd.SubmitWorkflow(wdl, inputs, options, id, time.Hour)
	if err != nil {
		return nil, err
	}
	return <-ctx.done, nil
}

func (kernel *Kernel) SubmitWorkflow(wdl, inputs, options string, id uuid.UUID, timeout time.Duration) (*WorkflowContext, error) {
	ctx, err := kernel.wd.SubmitWorkflow(wdl, inputs, options, id, timeout)
	if err != nil {
		return nil, err
	}
	return ctx, nil
}

func (kernel *Kernel) AbortWorkflow(uuid uuid.UUID) error {
	return nil
}

func (kernel *Kernel) ListWorkflows() []uuid.UUID {
	return nil
}

func (kernel *Kernel) Shutdown() {
}
