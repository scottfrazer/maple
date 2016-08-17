package maple

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

type WorkflowSources struct {
	wdl     string
	inputs  string
	options string
	_graph  *Graph
}

func (sources *WorkflowSources) graph() *Graph {
	if sources._graph != nil {
		return sources._graph
	}

	reader := strings.NewReader(sources.wdl)
	sources._graph = LoadGraph(reader)
	return sources._graph
}

type JobInstance struct {
	primaryKey int64
	node       *Node
	shard      int
	attempt    int
	status     string
	cancel     func()
	abort      func()
	wi         *WorkflowInstance
	db         *MapleDb
	log        *Logger
}

func (ji *JobInstance) setStatus(status string) {
	if status == ji.status {
		ji.log.Info("warning: already has status %s", status)
		return
	}
	ji.db.SetJobStatus(ji.primaryKey, status, ji.log)
	ji.log.Info("status change %s -> %s", ji.status, status)
	ji.status = status
}

func (ji *JobInstance) run(backend Backend, cmd *exec.Cmd, done chan<- *JobInstance, ctx context.Context) {
	var backendJobDone = make(chan bool, 1)
	var isAborting = false
	log := ji.log
	log.Info("run: enter")
	defer log.Info("run: exit")
	backendJobCtx, backendJobCancel := context.WithCancel(ctx)

	ji.setStatus("Started")

	backend.Submit(backendJobDone, backendJobCtx)

	for {
		select {
		case <-backendJobDone:
			ji.setStatus("Done")
			done <- ji
			return
		case <-ctx.Done():
			if !isAborting {
				log.Info("run: abort")
				backendJobCancel()
				isAborting = true
			}
		}
	}
}

type WorkflowInstance struct {
	uuid         uuid.UUID
	primaryKey   int64
	done         chan *WorkflowInstance
	source       *WorkflowSources
	status       string
	jobs         []*JobInstance
	jobsMutex    *sync.Mutex
	cancel       func()
	abort        func()
	shuttingDown bool
	aborting     bool
	db           *MapleDb
	log          *Logger
}

func (wi *WorkflowInstance) Uuid() uuid.UUID {
	return wi.uuid
}

func (wi *WorkflowInstance) Status() string {
	return wi.status
}

// TODO: this algorithm could use some work
func (wi *WorkflowInstance) isTerminal() *string {
	wi.jobsMutex.Lock()
	defer wi.jobsMutex.Unlock()

	status := "Done"

	for _, job := range wi.jobs {
		if job.status != "Done" {
			status = "Failed"
		}
		if job.status == "Started" || job.status == "NotStarted" {
			return nil
		}
	}
	return &status
}

func (wi *WorkflowInstance) setStatus(status string) {
	if status == wi.status {
		wi.log.Info("warning: already has status %s", status)
		return
	}
	wi.log.Info("status change %s -> %s", wi.status, status)
	wi.db.SetWorkflowStatus(wi.primaryKey, status, wi.log)
	wi.status = status
}

func (wi *WorkflowInstance) newJob(node *Node) (*JobInstance, error) {
	job := JobInstance{
		primaryKey: -1,
		node:       node,
		shard:      0,
		attempt:    1,
		status:     "NotStarted",
		cancel:     func() {},
		abort:      func() {},
		wi:         wi,
		db:         wi.db,
		log:        wi.log.ForJob(wi.uuid, node.name)}
	err := wi.db.NewJob(&job, wi.log)
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (wi *WorkflowInstance) isAcceptingJobs() bool {
	wi.jobsMutex.Lock()
	defer wi.jobsMutex.Unlock()
	return wi.shuttingDown == false && wi.aborting == false
}

func (wi *WorkflowInstance) doneJobsHandler(doneJobs <-chan *JobInstance, runnableJobs chan<- *JobInstance, workflowDone chan<- string) {
	graph := wi.source.graph()

	var launch = func(job *JobInstance) {
		if wi.isAcceptingJobs() {
			runnableJobs <- job
		}
	}

	var persist = func(nodes []*Node) []*JobInstance {
		jobs := make([]*JobInstance, len(nodes))
		for index, node := range nodes {
			job, err := wi.newJob(node)
			if err != nil {
				// TODO: don't panic, should fail workflow
				panic(err)
			}
			wi.jobsMutex.Lock()
			jobs[index] = job
			wi.jobs = append(wi.jobs, job)
			wi.jobsMutex.Unlock()
		}
		return jobs
	}

	if len(wi.jobs) > 0 {
		// Resuming jobs
		for _, job := range wi.jobs {
			if job.status == "Started" {
				launch(job)
			}
		}
	} else {
		// Launching the root jobs
		newJobs := persist(graph.Root())
		for _, job := range newJobs {
			launch(job)
		}
	}

	for doneJob := range doneJobs {
		// Persist all new downstream nodes to the database first
		jobs := persist(graph.Downstream(doneJob.node))

		// If at this stage every job is in a terminal state, the workflow is done.
		terminalStatus := wi.isTerminal()
		if terminalStatus != nil {
			wi.setStatus(*terminalStatus)
			workflowDone <- *terminalStatus
			return
		}

		// Use goroutine to submit jobs to the channel so this goroutine doesn't block
		// TODO: no exit strategy!  Have this take a context.Context and use select {}
		go func(newJobs []*JobInstance) {
			for _, job := range newJobs {
				launch(job)
			}
		}(jobs)
	}
}

func (wi *WorkflowInstance) run(done chan<- *WorkflowInstance, ctx context.Context, abortCtx context.Context) {
	var log = wi.log.ForWorkflow(wi.uuid)
	backend := NewLocalBackend()

	log.Info("run: enter")
	defer log.Info("run: exit")

	wi.setStatus("Started")

	var jobDone = make(chan *JobInstance)
	var workflowDone = make(chan string)
	var runnableJobs = make(chan *JobInstance)
	var doneJobs = make(chan *JobInstance)

	defer func() {
		log.Info("workflow: exiting with state: %s", wi.status)
		wi.done <- wi
		close(wi.done)
		close(doneJobs)
		done <- wi
	}()

	go wi.doneJobsHandler(doneJobs, runnableJobs, workflowDone)

	for {
		if wi.isAcceptingJobs() == false {
			select {
			case <-runnableJobs:
			case <-workflowDone:
				return
			case job := <-jobDone:
				log.Info("workflow: job finished: %s", job.status)
				doneJobs <- job
			}
		} else {
			select {
			case job := <-runnableJobs:
				log.Info("workflow: launching call: %s", job.node.String())
				ctx, cancel := context.WithCancel(context.Background())
				job.cancel = cancel
				go job.run(backend, exec.Command("sleep", "2"), jobDone, ctx)
			case <-workflowDone:
				return
			case job := <-jobDone:
				log.Info("workflow: job finished: %s", job.status)
				doneJobs <- job
			case <-ctx.Done():
				log.Info("workflow: shutdown...")
				wi.jobsMutex.Lock()
				wi.shuttingDown = true
				for _, job := range wi.jobs {
					job.cancel()
				}
				wi.jobsMutex.Unlock()
			case <-abortCtx.Done():
				log.Info("workflow: abort...")
				wi.jobsMutex.Lock()
				wi.aborting = true
				for _, job := range wi.jobs {
					job.abort()
				}
				wi.jobsMutex.Unlock()
			}
		}
	}
}

type Kernel struct {
	log                *Logger
	db                 *MapleDb
	start              time.Time
	on                 bool
	maxWorkers         int
	submitChannel      chan *WorkflowInstance
	mutex              *sync.Mutex
	abortChannel       chan *WorkflowInstance
	running            map[*WorkflowInstance]struct{}
	cancel             func()
	waitGroup          *sync.WaitGroup
	workflowMaxRuntime time.Duration
}

func (kernel *Kernel) run(ctx context.Context) {
	var workers = 0
	var workflowDone = make(chan *WorkflowInstance)
	var log = kernel.log

	log.Info("kernel: enter")
	defer func() {
		kernel.mutex.Lock()
		kernel.on = false
		kernel.mutex.Unlock()
		for wi, _ := range kernel.running {
			wi.cancel()
		}

		// TODO: add a waitGroup to wait for all workflow goroutines to exit
		kernel.waitGroup.Done()
		log.Info("kernel: exit")
	}()

	go func() {
		// TODO: returns PKs?  then loadWorkflow(workflowPk)
		restartable, _ := kernel.db.GetWorkflowsByStatus(log, "Started")
		for _, wi := range restartable {
			log.Info("kernel: resume workflow %s", wi.uuid)
			kernel.enqueue(wi, time.Minute)
		}
	}()

	var processDone = func(result *WorkflowInstance) {
		log.Info("kernel: workflow %s finished: %s", result.uuid, result.status)
		delete(kernel.running, result)
		workers--
	}

	for {
		if kernel.on == false {
			if len(kernel.running) == 0 {
				return
			}
			select {
			case wi := <-workflowDone:
				processDone(wi)
			}
		} else if workers < kernel.maxWorkers {
			select {
			case wi, ok := <-kernel.submitChannel:
				if ok {
					workers++
					kernel.running[wi] = struct{}{}
					wiCtx, workflowCancel := context.WithTimeout(ctx, kernel.workflowMaxRuntime)
					wiAbortCtx, workflowAbort := context.WithTimeout(ctx, kernel.workflowMaxRuntime)
					wi.cancel = workflowCancel
					wi.abort = workflowAbort
					log.Info("kernel: starting %s", wi.uuid)
					go wi.run(workflowDone, wiCtx, wiAbortCtx)
				}
			case abortUuid := <-kernel.abortChannel:
				for context, _ := range kernel.running {
					if context.uuid == abortUuid.uuid {
						context.cancel()
					}
				}
			case wi := <-workflowDone:
				processDone(wi)
			case <-ctx.Done():
				return
			}
		} else {
			select {
			case wi := <-workflowDone:
				processDone(wi)
			case <-ctx.Done():
				return
			}
		}
	}
}

// TODO: do I still need this?
func (kernel Kernel) signalHandler() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func(kernel Kernel) {
		sig := <-sigs
		kernel.log.Info("%s signal detected... turning off kernel", sig)
		kernel.cancel()
		kernel.log.Info("%s signal detected... kernel off", sig)
		os.Exit(130)
	}(kernel)
}

func (kernel *Kernel) enqueue(ctx *WorkflowInstance, timeout time.Duration) error {
	kernel.mutex.Lock()
	defer kernel.mutex.Unlock()
	if kernel.on == true {
		select {
		case kernel.submitChannel <- ctx:
		case <-time.After(timeout):
			return errors.New("Timeout submitting workflow")
		}
	} else {
		return errors.New("workflow submission is closed")
	}
	return nil
}

func (kernel *Kernel) newWorkflow(uuid uuid.UUID, source *WorkflowSources) (*WorkflowInstance, error) {
	var jobsMutex sync.Mutex

	wi := &WorkflowInstance{
		uuid:         uuid,
		primaryKey:   -1,
		done:         make(chan *WorkflowInstance, 1),
		source:       source,
		status:       "NotStarted",
		jobs:         nil,
		jobsMutex:    &jobsMutex,
		cancel:       func() {},
		abort:        func() {},
		shuttingDown: false,
		aborting:     false,
		db:           kernel.db,
		log:          kernel.log.ForWorkflow(uuid)}

	err := kernel.db.NewWorkflow(wi, kernel.log)
	if err != nil {
		return nil, err
	}

	return wi, nil
}

func NewKernel(log *Logger, dbName string, dbConnection string, concurrentWorkflows int, submitQueueSize int) *Kernel {
	var wg sync.WaitGroup
	var mutex sync.Mutex
	// TODO: move DB creation into On()?  Semantics should be: zero DB connections on Off()
	db := NewMapleDb(dbName, dbConnection, log)
	kernel := Kernel{
		log:                log,
		db:                 db,
		start:              time.Now(),
		on:                 false,
		maxWorkers:         concurrentWorkflows,
		submitChannel:      make(chan *WorkflowInstance, submitQueueSize),
		mutex:              &mutex,
		abortChannel:       make(chan *WorkflowInstance),
		running:            make(map[*WorkflowInstance]struct{}),
		cancel:             func() {},
		waitGroup:          &wg,
		workflowMaxRuntime: time.Second * 600}
	return &kernel
}

func (kernel *Kernel) On() {
	if kernel.on == true {
		return
	}

	kernel.mutex.Lock()
	defer kernel.mutex.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	kernel.on = true
	kernel.cancel = cancel
	kernel.waitGroup.Add(1)
	go kernel.run(ctx)
	kernel.signalHandler() // TODO: make this part of kernel.run(), also make it shutdown on Off()
}

func (kernel *Kernel) Off() {
	if kernel.on == false {
		return
	}

	kernel.cancel()
	kernel.waitGroup.Wait()
}

func (kernel *Kernel) Run(wdl, inputs, options string, id uuid.UUID) (*WorkflowInstance, error) {
	wi, err := kernel.Submit(wdl, inputs, options, id, time.Hour)
	if err != nil {
		return nil, err
	}
	return <-wi.done, nil
}

func (kernel *Kernel) Submit(wdl, inputs, options string, id uuid.UUID, timeout time.Duration) (*WorkflowInstance, error) {
	sources := WorkflowSources{strings.TrimSpace(wdl), strings.TrimSpace(inputs), strings.TrimSpace(options), nil}
	wi, err := kernel.newWorkflow(id, &sources)
	if err != nil {
		return nil, err
	}
	kernel.enqueue(wi, timeout)
	return wi, nil
}

func (kernel *Kernel) Abort(id uuid.UUID, timeout time.Duration) error {
	for context, _ := range kernel.running {
		if context.uuid == id {
			timer := time.After(timeout)

			select {
			case kernel.abortChannel <- context:
			case <-timer:
				return errors.New(fmt.Sprintf("Timeout submitting workflow %s to be aborted", context.uuid))
			}

			select {
			case <-context.done:
			case <-timer:
				return errors.New(fmt.Sprintf("Timeout aborting workflow %s", context.uuid))
			}
		}
	}
	return nil
}

func (kernel *Kernel) AbortCall(id uuid.UUID, timeout time.Duration) error {
	return nil
}

func (kernel *Kernel) List() ([]*WorkflowInstance, error) {
	return kernel.db.GetWorkflowsByStatus(kernel.log, "Started", "NotStarted", "Done", "Aborted")
}

func (kernel *Kernel) Uptime() time.Duration {
	return time.Since(kernel.start)
}
