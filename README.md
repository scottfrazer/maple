```
   __  ___          __
  /  |/  /__ ____  / /__
 / /|_/ / _ `/ _ \/ / -_)
/_/  /_/\_,_/ .__/_/\__/
           /_/
```

[![stability-experimental](https://img.shields.io/badge/stability-experimental-orange.svg)](https://github.com/emersion/stability-badges#experimental)

Server Design
=============

```
+-----------------------------------------------------------+
|                                                           |
| Command Line Interface (cmd/maple/main.go)                |
|                                                           |
+--------------------+-----^--------------------------------+
                     |     |
                     |     | HTTP API
                     |     |
+--------------------v-----+------------------------+-------+
|                                                   |       |
| Http Server (http.go)                             |       |
|                                                   |       |
+---------------------------------------------------+       |
|                                                   |       |
| Kernel (kernel.go)                                |       |
|                                                   |  Log  |
+--------------------------+------------------------+       |
|                          |                        |       |
| Database (db.go)         | Backend (backend.go)   |       |
|                          |                        |       |
+---------------------------------------------------+       |
                           |                        |       |
                           | UNIX / AWS process     |       |
                           |                        |       |
                           +------------------------+-------+
```

Kernel (kernel.go)
==================

The Kernel API will be a Go-only API that will allow submission, manipulation, and querying workflows.

The Kernel is the only Maple API that's exported.  The Kernel will call into the Database layer.  Kernel should allow only non-destructive type pass-through functions, like `GetWorkflowStatus()`, but not `SetWorkflowStatus()`, and perhaps some minor repackaging of data (e.g. `SnapshotOf()` returning a `*WorkflowInstance`)

"shutdown" refers to the tearing down of all goroutines and freeing up of resources that the kernel is holding onto.  On() Off() On() should act as if it was never shut down: it should resume running workflows, attaching to the jobs which should still be alive.

"abort" only refers to a particular workflow or job, a kernel cannot be aborted.  If kernel.Off() is called when a workflow is aborting, kernel.On() should resume aborting the workflow.  Aborting a workflow will abort all jobs then mark itself as aborted.  Aborting a job will free the resources associated with that job (kill the subprocess, decomission VMs, shutdown EC2 nodes, etc).

Example API:

```
NewKernel(settings...) *Kernel
kernel.On() - start goroutines, re-attach to existing jobs, start accepting submissions
kernel.Off() - shut down all goroutines, stop accepting submissions, leave jobs running.
               calls to Submit(), Run() return fast with error.  Should return when all
               goroutines shut down
kernel.AcceptSubmissions(bool) - calls to Submit(), Run() return error fast when 'false'

kernel.Abort(uuid) - abort single workflow
kernel.AbortCall(uuid, fqn string)
kernel.Run(wdl, inputs, options string) *WorkflowInstance // returns when workflow finishes
kernel.Submit(...) uuid // same as above, but returns quick once it's scheduled.
kernel.SnapshotOf(uuid) *WorkflowInstance
kernel.List() []uuid
```

Workflow States:

```
States: NotStarted -> Started -> [Aborted|Done|Failed]
                   -> Aborted
```

On Startup (always):

* Check database state, fix anything weird (maybe server was shutdown prematurely)
* Handle workflow FIFOs... maybe just reconnect to them, maybe remove them

Database (db.go)
================

Only called by the Kernel to persist data.  Nothing exported.  All pointers are 

```
NewMapleDb(settings...) *MapleDb

db.GetJobStatus()
db.NewWorkflow()
db.NewJob()
db.SetWorkflowStatus()
```

Backend
=======

ONE backend per workflow.  Backend and filesystem are married.  Backends are identified by strings like "SGE+unixfs", "GCE", "AWS", "local".  Backends need to know how to return Readers and Writers for all files.  A backend is instantiated per workflow invocation so it can take advantage of workflow options

Optionally, some tasks can be run inline (locally), but only if certain criteria are met:

1) No File inputs/outputs

```
interface MapleBackend {
  	Run(cmd, fqn string) handle
  	Abort(h handle)
}

interface WdlFile {
	Read(p []byte) (n int, err error)
	Write(p []byte) (n int, err error)
}

NewLocalBackend(wf *WorkflowInstance, settings...) *MapleBackend
backend.DbInit() error
backend.Run(job *JobInstance) handle
backend.Abort(h handle)
backend.Results(h handle) []*WdlFile
backend.Status(h handle) string
backend.Wait(h handle)
backend.JobFile(job *JobInstance, relpath string) *WdlFile
backend.File(abspath string) *WdlFile
```

Command Line Interface
=======================

### maple submit

```
$ maple submit foo.wdl foo.inputs
cefd19cb-a8b1-474d-bf58-9c4522a5af98
```
(Sends POST /submit)

### maple tail

```
$ maple tail [-n 10] [-f] cefd19cb
... tailed log lines ...
```
(Sends GET /tail/cefd19cb)

Should have behavior of UNIX tail:
    1) by default, return last 10 lines
        /tail/cefd19cb
        /tail/
    2) -n 1000 returns last 1000 lines and exit
        /tail/cefd19cb?n=1000
        /tail?n=1000
    3) -f streams lines (-n compatible) will show last n-lines
        /tail/cefd19cb?f=true
        /tail?f=true&n=1000

###  maple run

```
$ maple run foo.wdl foo.inputs [--tags=tag]
... same output as 'submit' then 'tail' ..
```

### maple ls

```
$ maple ls [--since=time|duration] [--status=running] [--tags=]
UUID        WDL       Status      Start Date
--------    -------   ---------   -------------------------
cefd19cb    foo.wdl   running     2016-08-10T01:39:32+00:00
93453875    bar.wdl   submitted   2016-08-10T01:39:32+00:00
9ce50dbf    baz.wdl   completed   2016-08-10T01:39:32+00:00
```
(Sends GET /workflows?since=&status=&tags=)

```
$ maple ls 93453875
FQN           Status    Backend   Start Date
------------  --------  --------  -------------------------
w.a           running   local     2016-08-10T01:39:32+00:00
w.b           running   local     2016-08-10T01:39:32+00:00
w.$scatter_0  completed           2016-08-10T01:39:32+00:00
```
(Sends GET /jobs?w=93453875)

```
$ maple ls 93453875 9ce50dbf
```
(Sends GET /jobs?w=93453875,9ce50dbf)
...

### maple show

```
$ maple show 93453875:w.a
Job 93453875:w.a
Start:       2016-08-10T01:39:32+00:00
End:
Status:      running
Backend:     local
Output Path: /var/maple/jobs/93453875/w.a

python <<CODE
  ...
CODE

=== INPUTS

File x = "/my/file.txt"
Array[Int] i = [0,1,2,3]

=== OUTPUTS

File log = stdout()
File result = "result.txt"
Int i = read_int("some_number.txt")

=== BACKEND

Name:        local
Pid:         65333
Return Code:
CPU Time:    1d4h27m
```

### maple abort

```
$ maple abort cefd19cb
```
(Sends POST /abort/cefd19cb)

### maple ping

```
$ maple ping
```
(Send GET /ping)

### maple server

```
$ maple server --port=8765
```

HTTP API (http.go)
==================

```
GET /ping
POST /submit
GET /tail/[:uuid]?n=10&f=false
GET /list[?q=cefd19cb,93453875]
GET /show/:uuid:fqn
POST /abort/:uuid[:fqn]
```

Badges
======

[![stability-unstable](https://img.shields.io/badge/stability-unstable-yellow.svg)](https://github.com/emersion/stability-badges#unstable)
[![stability-stable](https://img.shields.io/badge/stability-stable-green.svg)](https://github.com/emersion/stability-badges#stable)
