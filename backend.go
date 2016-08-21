package maple

import (
	"golang.org/x/net/context"
	"strconv"
	"sync"
	"time"
)

type JobHandle interface {
	String() string
}

type LocalJobHandle struct {
	id int
}

func (handle *LocalJobHandle) String() string {
	return strconv.FormatInt(int64(handle.id), 10)
}

type TestBackendJob struct {
	ctx      context.Context
	abortCtx context.Context
	ticker   <-chan time.Time
}

type Backend interface {
	Init() error
	Submit(job *JobInstance, done chan<- bool, ctx context.Context, abortCtx context.Context) JobHandle
}

type TestBackend struct {
	counter           int
	jobs              map[int]*TestBackendJob
	jobsMutex         *sync.Mutex
	defaultJobRuntime time.Duration
	jobRuntime        map[string]time.Duration
}

func NewTestBackend(defaultJobRuntime time.Duration, jobRuntime map[string]time.Duration) Backend {
	var mutex sync.Mutex
	be := TestBackend{0, make(map[int]*TestBackendJob), &mutex, defaultJobRuntime, jobRuntime}
	return be
}

func (be TestBackend) Init() error {
	return nil
}

func (be TestBackend) Submit(job *JobInstance, done chan<- bool, ctx context.Context, abortCtx context.Context) JobHandle {
	be.jobsMutex.Lock()
	defer be.jobsMutex.Unlock()

	runtime := be.defaultJobRuntime
	// TODO: job.node().name should be job.Node().Name()
	if val, ok := be.jobRuntime[job.node().name]; ok {
		runtime = val
	}

	backendJob := TestBackendJob{ctx, abortCtx, time.After(runtime)}
	handle := &LocalJobHandle{be.counter}
	be.jobs[be.counter] = &backendJob
	be.counter += 1

	go func(job *TestBackendJob) {
		select {
		case <-job.ticker:
		case <-job.ctx.Done():
		case <-job.abortCtx.Done():
		}

		//cmd.Run()

		be.jobsMutex.Lock()
		delete(be.jobs, handle.id)
		be.jobsMutex.Unlock()
		done <- true
	}(&backendJob)

	return handle
}

func Abort(handle *JobHandle) {
	/*err := cmd.Process.Kill()
	if err != nil {
		panic(err)
	}*/
}

func (be *TestBackend) Wait(handle JobHandle) {

}

func (be *TestBackend) Recover(handle JobHandle) {

}
