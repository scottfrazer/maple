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

type JobInstance struct {
	entry  *JobEntry
	cancel func()
	abort  func()
	wi     *WorkflowInstance
	db     *MapleDb
	log    *Logger
}

func (ji *JobInstance) status() string {
	return ji.entry.LatestStatusEntry().status
}

func (ji *JobInstance) node() *Node {
	return ji.wi.Graph().Find(ji.entry.fqn)
}

func (ji *JobInstance) setStatus(status string) error {
	if status == ji.status() {
		ji.log.Info("warning: already has status %s", status)
		return nil
	}
	ji.log.Info("status change %s -> %s", ji.status(), status)
	statusEntry, err := ji.db.NewJobStatusEntry(ji.entry.primaryKey, status, time.Now(), ji.log)
	if err != nil {
		return err
	}
	ji.entry.statusEntries = append(ji.entry.statusEntries, statusEntry)
	return nil
}

func (ji *JobInstance) run(backend Backend, cmd *exec.Cmd, done chan<- *JobInstance, ctx context.Context) {
	var backendJobDone = make(chan bool, 1)
	log := ji.log
	log.Info("run: enter")
	defer log.Info("run: exit")
	backendJobCtx, _ := context.WithCancel(ctx)

	ji.setStatus("Started")

	backend.Submit(backendJobDone, backendJobCtx)

	for {
		select {
		case <-backendJobDone:
			ji.setStatus("Done")
			done <- ji
			return
		case <-ctx.Done():
			log.Info("run: shutdown...")
			<-backendJobDone
			return
		}
	}
}

type WorkflowInstance struct {
	entry        *WorkflowEntry
	done         chan *WorkflowInstance
	jobs         []*JobInstance
	jobsMutex    *sync.Mutex
	cancel       func()
	abort        func()
	shuttingDown bool
	aborting     bool
	db           *MapleDb
	log          *Logger
	start        time.Time // TODO: eventually move this to the entry (and DB)
	_graph       *Graph
}

func (wi *WorkflowInstance) Uuid() uuid.UUID {
	return wi.entry.uuid
}

func (wi *WorkflowInstance) Status() string {
	return wi.entry.LatestStatusEntry().status
}

func (wi *WorkflowInstance) Graph() *Graph {
	if wi._graph == nil {
		reader := strings.NewReader(wi.entry.sources.wdl)
		wi._graph = LoadGraph(reader)
	}
	return wi._graph
}

func (wi *WorkflowInstance) isTerminal() *string {
	wi.jobsMutex.Lock()
	defer wi.jobsMutex.Unlock()

	status := "Done"

	for _, job := range wi.jobs {
		if job.status() != "Done" {
			status = "Failed"
		}
		if job.status() == "Started" || job.status() == "NotStarted" {
			return nil
		}
	}
	return &status
}

func (wi *WorkflowInstance) setStatus(status string) error {
	if status == wi.Status() {
		wi.log.Info("warning: already has status %s", status)
		return nil
	}
	wi.log.Info("status change %s -> %s", wi.Status(), status)
	statusEntry, err := wi.db.NewWorkflowStatusEntry(wi.entry.primaryKey, status, time.Now(), wi.log)
	if err != nil {
		return err
	}
	wi.entry.statusEntries = append(wi.entry.statusEntries, statusEntry)
	return nil
}

func (wi *WorkflowInstance) newJobInstance(node *Node) (*JobInstance, error) {
	entry, err := wi.db.NewJobEntry(wi.entry.primaryKey, node.name, 0, 1, wi.log)
	if err != nil {
		return nil, err
	}

	return wi.jobInstanceFromJobEntry(entry), nil
}

func (wi *WorkflowInstance) jobInstanceFromJobEntry(entry *JobEntry) *JobInstance {
	return &JobInstance{
		entry:  entry,
		cancel: func() {},
		abort:  func() {},
		wi:     wi,
		db:     wi.db,
		log:    wi.log.ForJob(wi.Uuid(), entry.fqn)}
}

func (wi *WorkflowInstance) isAcceptingJobs() bool {
	wi.jobsMutex.Lock()
	defer wi.jobsMutex.Unlock()
	return wi.shuttingDown == false && wi.aborting == false
}

func (wi *WorkflowInstance) doneJobsHandler(doneJobs <-chan *JobInstance, runnableJobs chan<- *JobInstance, workflowDone chan<- string) {
	var launch = func(job *JobInstance) {
		if wi.isAcceptingJobs() {
			runnableJobs <- job
		}
	}

	var persist = func(nodes []*Node) []*JobInstance {
		jobs := make([]*JobInstance, len(nodes))
		for index, node := range nodes {
			job, err := wi.newJobInstance(node)
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

	if len(wi.entry.jobs) > 0 {
		// Resuming jobs
		for _, jobEntry := range wi.entry.jobs {
			if jobEntry.LatestStatusEntry().status == "Started" {
				ji := wi.jobInstanceFromJobEntry(jobEntry)
				launch(ji)
			}
		}
	} else {
		// Launching the root jobs
		newJobs := persist(wi.Graph().Root())
		for _, job := range newJobs {
			launch(job)
		}
	}

	for doneJob := range doneJobs {
		// Persist all new downstream nodes to the database first
		jobs := persist(wi.Graph().Downstream(doneJob.node()))

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

func (wi *WorkflowInstance) run(done chan<- *WorkflowInstance, ctx context.Context, abortCtx context.Context, wg *sync.WaitGroup) {
	var log = wi.log.ForWorkflow(wi.Uuid())
	backend := NewLocalBackend()

	wi.start = time.Now()
	log.Info("run: enter")
	defer func() {
		log.Info("run: exit (runtime %s)", time.Since(wi.start))
	}()

	wi.setStatus("Started")

	var jobDone = make(chan *JobInstance)
	var workflowDone = make(chan string)
	var runnableJobs = make(chan *JobInstance)
	var doneJobs = make(chan *JobInstance)

	defer func() {
		log.Info("workflow: exiting with state: %s", wi.Status())
		close(doneJobs) // This will cause the doneJobsHandler() goroutine to exit
		if !wi.shuttingDown {
			wi.done <- wi
			close(wi.done)
			done <- wi
		}
		wg.Done()
	}()

	go wi.doneJobsHandler(doneJobs, runnableJobs, workflowDone)

	for {
		if wi.isAcceptingJobs() == false {
			select {
			case <-runnableJobs:
			case <-workflowDone:
				return
			case job := <-jobDone:
				log.Info("workflow: job finished: %s", job.status())
				doneJobs <- job
			}
		} else {
			select {
			case job := <-runnableJobs:
				log.Info("workflow: launching call: %s", job.node().String())
				jobCtx, jobCancel := context.WithCancel(ctx)
				job.cancel = jobCancel
				go job.run(backend, exec.Command("sleep", "2"), jobDone, jobCtx)
			case <-workflowDone:
				return
			case job := <-jobDone:
				log.Info("workflow: job finished: %s", job.status())
				doneJobs <- job
			case <-ctx.Done():
				log.Info("workflow: shutdown...")
				wi.jobsMutex.Lock()
				wi.shuttingDown = true
				wi.jobsMutex.Unlock()
				return
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
	var workflowWaitGroup sync.WaitGroup
	var workers = 0
	var workflowDone = make(chan *WorkflowInstance)
	var log = kernel.log

	log.Info("kernel: enter")
	defer func() {
		kernel.mutex.Lock()
		kernel.on = false
		kernel.mutex.Unlock()
		workflowWaitGroup.Wait()
		kernel.waitGroup.Done()
		log.Info("kernel: exit")
	}()

	go func() {
		resumableEntries, err := kernel.db.LoadWorkflowsByStatus(log, "Started")
		if err != nil {
			panic(err) // TODO don't panic
		}
		for _, entry := range resumableEntries {
			log.Info("kernel: resume workflow %s", entry.uuid)
			wi := kernel.newWorkflowInstanceFromEntry(entry)
			kernel.enqueue(wi, time.Minute)
		}
	}()

	var processDone = func(result *WorkflowInstance) {
		log.Info("kernel: workflow %s finished: %s", result.Uuid(), result.Status())
		delete(kernel.running, result)
		workers--
	}

	for {
		// TODO: need mutex around kernel.on
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
					wiCtx, workflowCancel := context.WithCancel(ctx)
					wiAbortCtx, workflowAbort := context.WithTimeout(context.Background(), kernel.workflowMaxRuntime)
					wi.cancel = workflowCancel
					wi.abort = workflowAbort
					log.Info("kernel: starting %s", wi.Uuid())
					workflowWaitGroup.Add(1)
					go wi.run(workflowDone, wiCtx, wiAbortCtx, &workflowWaitGroup)
				}
			case abortWi := <-kernel.abortChannel:
				for wi, _ := range kernel.running {
					if wi.Uuid() == abortWi.Uuid() {
						wi.abort()
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
		kernel.waitGroup.Wait()
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

func (kernel *Kernel) newWorkflowInstance(uuid uuid.UUID, source *WorkflowSources) (*WorkflowInstance, error) {
	entry, err := kernel.db.NewWorkflowEntry(uuid, source.wdl, source.inputs, source.options, kernel.log)
	if err != nil {
		return nil, err
	}
	return kernel.newWorkflowInstanceFromEntry(entry), nil
}

func (kernel *Kernel) newWorkflowInstanceFromEntry(entry *WorkflowEntry) *WorkflowInstance {
	var jobsMutex sync.Mutex

	return &WorkflowInstance{
		entry:        entry,
		done:         make(chan *WorkflowInstance, 1),
		jobs:         nil,
		jobsMutex:    &jobsMutex,
		cancel:       func() {},
		abort:        func() {},
		shuttingDown: false,
		aborting:     false,
		db:           kernel.db,
		log:          kernel.log.ForWorkflow(entry.uuid),
		_graph:       nil}
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
	wi, err := kernel.newWorkflowInstance(id, &sources)
	if err != nil {
		return nil, err
	}
	kernel.enqueue(wi, timeout)
	return wi, nil
}

func (kernel *Kernel) Abort(id uuid.UUID, timeout time.Duration) error {
	for wi, _ := range kernel.running {
		if wi.Uuid() == id {
			timer := time.After(timeout)

			select {
			case kernel.abortChannel <- wi:
			case <-timer:
				return errors.New(fmt.Sprintf("Timeout submitting workflow %s to be aborted", wi.Uuid()))
			}

			select {
			case <-wi.done:
			case <-timer:
				return errors.New(fmt.Sprintf("Timeout aborting workflow %s", wi.Uuid()))
			}
		}
	}
	return nil
}

func (kernel *Kernel) AbortCall(id uuid.UUID, timeout time.Duration) error {
	return nil
}

func (kernel *Kernel) List() ([]*WorkflowEntry, error) {
	return kernel.db.LoadWorkflowsByStatus(kernel.log, "Started", "NotStarted", "Done", "Aborted")
}

func (kernel *Kernel) Uptime() time.Duration {
	return time.Since(kernel.start)
}
