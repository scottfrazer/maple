package maple

import (
	"database/sql"
	"errors"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"github.com/satori/go.uuid"
	"strconv"
	"strings"
	"sync"
	"time"
)

type MapleDb struct {
	driverName     string
	dataSourceName string
	log            *Logger
	db             *sql.DB
	mtx            *sync.Mutex
}

func NewMapleDb(driverName, dataSourceName string, log *Logger) *MapleDb {
	db, err := sql.Open(driverName, dataSourceName)
	if err != nil {
		panic(err)
	}
	var mtx sync.Mutex
	dsp := &MapleDb{driverName, dataSourceName, log, db, &mtx}
	dsp.setup()
	return dsp
}

func (dsp *MapleDb) Close() {
	// TODO: close dsp.db
}

func (dsp *MapleDb) tables() ([]string, error) {
	query := "SELECT name FROM sqlite_master WHERE type='table';"
	dsp.log.DbQuery(query)
	rows, err := dsp.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		err = rows.Scan(&name)
		if err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}

	err = rows.Err()
	if err != nil {
		return nil, err
	}

	return tables, nil
}

func (dsp *MapleDb) query(query string) {
	dsp.log.DbQuery(query)
	_, err := dsp.db.Exec(query)
	if err != nil {
		panic(err)
	}
}

func (dsp *MapleDb) setup() {
	tableNames, err := dsp.tables()
	if err != nil {
		panic(err)
	}

	if !contains("workflow", tableNames) {
		dsp.query(`CREATE TABLE workflow (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			uuid TEXT
		);`)
	}

	if !contains("workflow_status", tableNames) {
		dsp.query(`CREATE TABLE workflow_status (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			workflow_id INTEGER NOT NULL,
			status TEXT,
			date TEXT,
			FOREIGN KEY(workflow_id) REFERENCES workflow(id)
		);`)
	}

	if !contains("job", tableNames) {
		dsp.query(`CREATE TABLE job (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			workflow_id INTEGER NOT NULL,
			call_fqn TEXT,
			shard INT,
			attempt INT,
			FOREIGN KEY(workflow_id) REFERENCES workflow(id)
		);`)
	}

	if !contains("job_status", tableNames) {
		dsp.query(`CREATE TABLE job_status (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			job_id INTEGER NOT NULL,
			status TEXT,
			date TEXT,
			FOREIGN KEY(job_id) REFERENCES job(id)
		);`)
	}

	if !contains("workflow_sources", tableNames) {
		dsp.query(`CREATE TABLE workflow_sources (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			workflow_id INTEGER NOT NULL,
			wdl TEXT,
			inputs TEXT,
			options TEXT,
			FOREIGN KEY(workflow_id) REFERENCES workflow(id)
		);`)
	}
}

func (dsp *MapleDb) NewJob(wfCtx *WorkflowInstance, node *Node, log *Logger) (*JobInstance, error) {
	dsp.mtx.Lock()
	defer dsp.mtx.Unlock()
	db := dsp.db
	var success = false
	var jobId int64 = -1

	tx, err := db.Begin()

	defer func() {
		if tx != nil {
			if success {
				tx.Commit()
			} else {
				tx.Rollback()
			}
		}
	}()

	if err != nil {
		return nil, err
	}

	query := `INSERT INTO job (workflow_id, call_fqn, shard, attempt) VALUES (?, ?, ?, ?)`
	log.DbQuery(query, strconv.FormatInt(wfCtx.primaryKey, 10), node.name, "0", "1")
	res, err := tx.Exec(query, wfCtx.primaryKey, node.name, 0, 1)
	if err != nil {
		return nil, err
	}

	jobId, err = res.LastInsertId()
	if err != nil {
		return nil, err
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}

	if rows != 1 {
		return nil, errors.New("could not insert into 'job' table")
	}

	now := time.Now().Format("2006-01-02 15:04:05.999")
	query = `INSERT INTO job_status (job_id, status, date) VALUES (?, 'NotStarted', ?)`
	log.DbQuery(query, strconv.FormatInt(jobId, 10), now)
	res, err = tx.Exec(query, jobId, now)
	if err != nil {
		return nil, err
	}

	rows, err = res.RowsAffected()
	if err != nil {
		return nil, err
	}

	if rows != 1 {
		return nil, errors.New("could not insert into 'job_status' table")
	}

	ctx := JobInstance{jobId, node, 0, 1, "NotStarted", func() {}, wfCtx, dsp, log}
	success = true
	return &ctx, nil
}

func (dsp *MapleDb) SetJobStatus(jobCtx *JobInstance, status string, log *Logger) (bool, error) {
	dsp.mtx.Lock()
	defer dsp.mtx.Unlock()
	db := dsp.db
	var nowISO8601 = time.Now().Format("2006-01-02 15:04:05.999")
	var query = `INSERT INTO job_status (job_id, status, date) VALUES (?, ?, ?)`
	log.DbQuery(query, strconv.FormatInt(jobCtx.primaryKey, 10), status, nowISO8601)
	_, err := db.Exec(query, jobCtx.primaryKey, status, nowISO8601)
	if err != nil {
		return false, err
	}
	jobCtx.status = status
	return true, nil
}

func (dsp *MapleDb) GetJobStatus(jobId int64, log *Logger) (string, error) {
	dsp.mtx.Lock()
	defer dsp.mtx.Unlock()
	return dsp._GetJobStatus(jobId, log)
}

func (dsp *MapleDb) NewWorkflow(uuid uuid.UUID, sources *WorkflowSources, log *Logger) (*WorkflowInstance, error) {
	dsp.mtx.Lock()
	defer dsp.mtx.Unlock()
	db := dsp.db
	var success = false
	var workflowId int64 = -1

	tx, err := db.Begin()

	defer func() {
		if tx != nil {
			if success {
				tx.Commit()
			} else {
				tx.Rollback()
			}
		}
	}()

	if err != nil {
		return nil, err
	}

	query := `INSERT INTO workflow (uuid) VALUES (?)`
	log.DbQuery(query, uuid.String())
	res, err := tx.Exec(query, uuid)
	if err != nil {
		return nil, err
	}

	workflowId, err = res.LastInsertId()
	if err != nil {
		return nil, err
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}

	if rows != 1 {
		return nil, errors.New("could not insert into 'workflow' table")
	}

	query = `INSERT INTO workflow_sources (workflow_id, wdl, inputs, options) VALUES (?, ?, ?, ?)`
	log.DbQuery(query, strconv.FormatInt(workflowId, 10), "{omit}", "{omit}", "{omit}")
	res, err = tx.Exec(query, workflowId, sources.wdl, sources.inputs, sources.options)
	if err != nil {
		return nil, err
	}

	rows, err = res.RowsAffected()
	if err != nil {
		return nil, err
	}

	if rows != 1 {
		return nil, errors.New("could not insert into 'workflow_sources' table")
	}

	now := time.Now().Format("2006-01-02 15:04:05.999")
	query = `INSERT INTO workflow_status (workflow_id, status, date) VALUES (?, 'NotStarted', ?)`
	log.DbQuery(query, strconv.FormatInt(workflowId, 10), now)
	res, err = tx.Exec(query, workflowId, now)
	if err != nil {
		return nil, err
	}

	rows, err = res.RowsAffected()
	if err != nil {
		return nil, err
	}

	if rows != 1 {
		return nil, errors.New("could not insert into 'workflow_status' table")
	}

	var jobsMutex sync.Mutex

	// TODO: some kind of constructor for this?
	ctx := WorkflowInstance{
		uuid, workflowId, make(chan *WorkflowInstance, 1),
		sources, "NotStarted", nil, &jobsMutex, func() {}, false, dsp, log.ForWorkflow(uuid)}
	success = true
	return &ctx, nil
}

func (dsp *MapleDb) LoadWorkflow(uuid uuid.UUID, log *Logger) (*WorkflowInstance, error) {
	dsp.mtx.Lock()
	defer dsp.mtx.Unlock()
	db := dsp.db
	var context WorkflowInstance
	var jobsMutex sync.Mutex

	context.uuid = uuid
	context.done = make(chan *WorkflowInstance)
	context.jobsMutex = &jobsMutex

	query := `SELECT id FROM workflow WHERE uuid=?`
	log.DbQuery(query, uuid.String())
	row := db.QueryRow(query, uuid)
	err := row.Scan(&context.primaryKey)
	if err != nil {
		return nil, err
	}

	return dsp._LoadWorkflowSources(log, &context, context.primaryKey)
}

func (dsp *MapleDb) SetWorkflowStatus(wfCtx *WorkflowInstance, status string, log *Logger) (bool, error) {
	dsp.mtx.Lock()
	defer dsp.mtx.Unlock()
	db := dsp.db
	var nowISO8601 = time.Now().Format("2006-01-02 15:04:05.999")
	var query = `INSERT INTO workflow_status (workflow_id, status, date) VALUES (?, ?, ?)`
	log.DbQuery(query, strconv.FormatInt(wfCtx.primaryKey, 10), status, nowISO8601)
	_, err := db.Exec(query, wfCtx.primaryKey, status, nowISO8601)
	if err != nil {
		return false, err
	}
	wfCtx.status = status
	return true, nil
}

func (dsp *MapleDb) GetWorkflowStatus(wfCtx *WorkflowInstance, log *Logger) (string, error) {
	dsp.mtx.Lock()
	defer dsp.mtx.Unlock()
	return dsp._GetWorkflowStatus(wfCtx, log)
}

func (dsp *MapleDb) GetWorkflowsByStatus(log *Logger, status ...string) ([]*WorkflowInstance, error) {
	dsp.mtx.Lock()
	defer dsp.mtx.Unlock()
	db := dsp.db
	questionMarks := make([]string, len(status))
	for i := 0; i < len(status); i++ {
		questionMarks[i] = "?"
	}
	var query = `SELECT workflow_id FROM (SELECT workflow_id, status, MAX(date) FROM workflow_status GROUP BY workflow_id) WHERE status IN (` + strings.Join(questionMarks, ", ") + `);`
	log.DbQuery(query, status...)

	queryParams := make([]interface{}, len(status))
	for i := range status {
		queryParams[i] = status[i]
	}

	rows, err := db.Query(query, queryParams...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contexts []*WorkflowInstance
	for rows.Next() {
		var id int64
		err = rows.Scan(&id)
		if err != nil {
			return nil, err
		}
		context, err := dsp._LoadWorkflowPK(log, id)
		if err != nil {
			return nil, err
		}
		contexts = append(contexts, context)
	}

	err = rows.Err()
	if err != nil {
		return nil, err
	}

	return contexts, nil
}

func (dsp *MapleDb) _GetWorkflowStatus(wfCtx *WorkflowInstance, log *Logger) (string, error) {
	db := dsp.db
	var query = `SELECT status FROM workflow_status WHERE workflow_id=? ORDER BY datetime(date) DESC, id DESC LIMIT 1`
	log.DbQuery(query, strconv.FormatInt(wfCtx.primaryKey, 10))
	row := db.QueryRow(query, wfCtx.primaryKey)
	var status string
	err := row.Scan(&status)
	if err != nil {
		return "", err
	}
	return status, nil
}

func (dsp *MapleDb) _GetJobStatus(jobId int64, log *Logger) (string, error) {
	db := dsp.db
	var query = `SELECT status FROM job_status WHERE job_id=? ORDER BY datetime(date) DESC, id DESC LIMIT 1`
	log.DbQuery(query, strconv.FormatInt(jobId, 10))
	row := db.QueryRow(query, jobId)
	var status string
	err := row.Scan(&status)
	if err != nil {
		return "", err
	}
	return status, nil
}

func (dsp *MapleDb) _LoadWorkflowPK(log *Logger, primaryKey int64) (*WorkflowInstance, error) {
	db := dsp.db
	// TODO: consolidate creation of WorkflowInstance.  e.g. hard to set ctx.jobsMutex
	var context WorkflowInstance
	var jobsMutex sync.Mutex
	context.jobsMutex = &jobsMutex
	context.primaryKey = primaryKey

	query := `SELECT uuid FROM workflow WHERE id=?`
	log.DbQuery(query, strconv.FormatInt(primaryKey, 10))
	row := db.QueryRow(query, primaryKey)
	err := row.Scan(&context.uuid)
	if err != nil {
		return nil, err
	}

	return dsp._LoadWorkflowSources(log, &context, primaryKey)
}

func (dsp *MapleDb) _LoadWorkflowSources(log *Logger, context *WorkflowInstance, primaryKey int64) (*WorkflowInstance, error) {
	db := dsp.db
	var sources WorkflowSources
	var err error

	context.done = make(chan *WorkflowInstance)
	context.status, err = dsp._GetWorkflowStatus(context, log)
	if err != nil {
		return nil, err
	}

	/* Load Sources */

	query := `SELECT wdl, inputs, options FROM workflow_sources WHERE workflow_id=?`
	log.DbQuery(query, strconv.FormatInt(context.primaryKey, 10))
	row := db.QueryRow(query, context.primaryKey)
	err = row.Scan(&sources.wdl, &sources.inputs, &sources.options)
	if err != nil {
		return nil, err
	}
	context.source = &sources

	/* Load Jobs */

	query = `SELECT id, call_fqn, shard, attempt FROM job WHERE workflow_id=?`
	log.DbQuery(query, strconv.FormatInt(context.primaryKey, 10))
	rows, err := dsp.db.Query(query, context.primaryKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*JobInstance
	for rows.Next() {
		var job JobInstance
		var name string

		err = rows.Scan(&job.primaryKey, &name, &job.shard, &job.attempt)
		if err != nil {
			return nil, err
		}

		graph := context.source.graph()
		node := graph.Find(name)
		if node == nil {
			return nil, errors.New(fmt.Sprintf("Node name %s invalid", name))
		}
		job.node = node

		status, err := dsp._GetJobStatus(job.primaryKey, log)
		if err != nil {
			return nil, err
		}
		job.status = status

		jobs = append(jobs, &job)
	}

	context.jobs = jobs

	return context, nil
}

func contains(a string, list []string) bool {
	for _, b := range list {
		if b == a {
			return true
		}
	}
	return false
}
