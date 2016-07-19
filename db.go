package main

import (
	"database/sql"
	"errors"
	_ "github.com/mattn/go-sqlite3"
	"github.com/satori/go.uuid"
	"strconv"
	"strings"
	"sync"
	"time"
)

type DatabaseDispatcher struct {
	driverName     string
	dataSourceName string
	queue          chan interface{}
	log            *Logger
	db             *sql.DB
	isRunning      bool
	abort          chan bool
	wg             *sync.WaitGroup
}

func NewDatabaseDispatcher(driverName, dataSourceName string, log *Logger) *DatabaseDispatcher {
	db, err := sql.Open(driverName, dataSourceName)
	if err != nil {
		panic(err)
	}
	abort := make(chan bool)
	queue := make(chan interface{})
	var wg sync.WaitGroup
	dbDispatcher := &DatabaseDispatcher{driverName, dataSourceName, queue, log, db, true, abort, &wg}
	go dbDispatcher.start()
	return dbDispatcher
}

func (dsp *DatabaseDispatcher) Abort() {
	close(dsp.abort)
	dsp.wg.Wait()
}

func (dsp *DatabaseDispatcher) tables() []string {
	query := "SELECT name FROM sqlite_master WHERE type='table';"
	dsp.log.DbQuery(query)
	rows, err := dsp.db.Query(query)
	e(err)
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		err = rows.Scan(&name)
		e(err)
		tables = append(tables, name)
	}

	err = rows.Err()
	e(err)

	return tables
}

func (dsp *DatabaseDispatcher) query(query string) {
	dsp.log.DbQuery(query)
	_, err := dsp.db.Exec(query)
	e(err)
}

func (dsp *DatabaseDispatcher) setup() {
	var tableNames = dsp.tables()

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

type DbNewWorkflow struct {
	log     *Logger
	uuid    uuid.UUID
	sources *WorkflowSources
	done    chan *WorkflowContext
}

type DbLoadWorkflow struct {
	log  *Logger
	uuid uuid.UUID
	done chan *WorkflowContext
}

type DbSetStatus struct {
	log    *Logger
	id     WorkflowIdentifier
	time   time.Time
	status string
	done   *chan bool
}

type DbGetStatus struct {
	log  *Logger
	id   WorkflowIdentifier
	done chan string
}

type DbGetByStatus struct {
	log      *Logger
	statuses []string
	done     chan []*WorkflowContext
}

func (dsp *DatabaseDispatcher) NewWorkflow(uuid uuid.UUID, sources *WorkflowSources, log *Logger) *WorkflowContext {
	done := make(chan *WorkflowContext, 1)
	dsp.queue <- DbNewWorkflow{log, uuid, sources, done}
	return <-done
}

func (dsp *DatabaseDispatcher) LoadWorkflow(uuid uuid.UUID, log *Logger) *WorkflowContext {
	done := make(chan *WorkflowContext, 1)
	dsp.queue <- DbLoadWorkflow{log, uuid, done}
	return <-done
}

func (dsp *DatabaseDispatcher) SetWorkflowStatus(wfId WorkflowIdentifier, status string, log *Logger) bool {
	done := make(chan bool, 1)
	dsp.queue <- DbSetStatus{log, wfId, time.Now(), status, &done}
	return <-done
}

func (dsp *DatabaseDispatcher) GetWorkflowStatus(wfId WorkflowIdentifier, log *Logger) string {
	done := make(chan string, 1)
	dsp.queue <- DbGetStatus{log, wfId, done}
	return <-done
}

func (dsp *DatabaseDispatcher) GetWorkflowsByStatus(log *Logger, status ...string) []*WorkflowContext {
	done := make(chan []*WorkflowContext, 1)
	dsp.queue <- DbGetByStatus{log, status, done}
	return <-done
}

func (dsp *DatabaseDispatcher) _NewWorkflow(req *DbNewWorkflow) (*WorkflowContext, error) {
	db := dsp.db
	log := req.log
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
	log.DbQuery(query, req.uuid.String())
	res, err := tx.Exec(query, req.uuid)
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
	log.DbQuery(query)
	res, err = tx.Exec(query, workflowId, req.sources.wdl, req.sources.inputs, req.sources.options)
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

	ctx := WorkflowContext{req.uuid, workflowId, make(chan *WorkflowContext, 1), req.sources, "NotStarted", nil}
	success = true
	return &ctx, nil
}

func (dsp *DatabaseDispatcher) _SetWorkflowStatus(req *DbSetStatus) {
	db := dsp.db
	log := req.log
	var nowISO8601 = time.Now().Format("2006-01-02 15:04:05.999")
	var query = `INSERT INTO workflow_status (workflow_id, status, date) VALUES (?, ?, ?)`
	log.DbQuery(query, strconv.FormatInt(req.id.dbKey(), 10), req.status, nowISO8601)
	_, err := db.Exec(query, req.id.dbKey(), req.status, nowISO8601)
	e(err)
}

func (dsp *DatabaseDispatcher) _GetWorkflowStatus(req *DbGetStatus) string {
	db := dsp.db
	log := req.log
	var query = `SELECT status FROM workflow_status WHERE workflow_id=? ORDER BY datetime(date) DESC, id DESC LIMIT 1`
	log.DbQuery(query, strconv.FormatInt(req.id.dbKey(), 10))
	row := db.QueryRow(query, req.id.dbKey())
	var status string
	err := row.Scan(&status)
	e(err)
	return status
}

func (dsp *DatabaseDispatcher) _GetWorkflowsByStatus(req *DbGetByStatus) ([]*WorkflowContext, error) {
	db := dsp.db
	log := req.log
	questionMarks := make([]string, len(req.statuses))
	for i := 0; i < len(req.statuses); i++ {
		questionMarks[i] = "?"
	}
	var query = `SELECT workflow_id FROM (SELECT workflow_id, status, MAX(date) FROM workflow_status GROUP BY workflow_id) WHERE status IN (` + strings.Join(questionMarks, ", ") + `);`
	log.DbQuery(query, req.statuses...)

	queryParams := make([]interface{}, len(req.statuses))
	for i := range req.statuses {
		queryParams[i] = req.statuses[i]
	}

	rows, err := db.Query(query, queryParams...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contexts []*WorkflowContext
	for rows.Next() {
		var id int64
		err = rows.Scan(&id)
		e(err)
		context, err := dsp._LoadWorkflowPK(log, id)
		e(err)
		contexts = append(contexts, context)
	}

	err = rows.Err()
	if err != nil {
		return nil, err
	}

	return contexts, nil
}

func (dsp *DatabaseDispatcher) start() {
	dsp.setup()

	defer func() {
		dsp.db.Close()
		dsp.isRunning = false
		dsp.wg.Done()
	}()

	for {
		select {
		case _, ok := <-dsp.abort:
			if !ok {
				return
			}
		case action := <-dsp.queue:
			switch t := action.(type) {
			case DbSetStatus:
				dsp._SetWorkflowStatus(&t)
				if t.done != nil {
					*t.done <- true
				}
			case DbGetStatus:
				t.done <- dsp._GetWorkflowStatus(&t)
			case DbGetByStatus:
				uuids, err := dsp._GetWorkflowsByStatus(&t)
				if err != nil {
					dsp.log.Info("[db] _GetWorkflowsByStatus failed: %s", err)
					continue
				}
				t.done <- uuids
			case DbNewWorkflow:
				ctx, err := dsp._NewWorkflow(&t)
				if err != nil {
					dsp.log.Info("[db] _NewWorkflow failed: %s", err)
					continue
				}
				t.done <- ctx
			case DbLoadWorkflow:
				ctx, err := dsp._LoadWorkflow(&t)
				if err != nil {
					dsp.log.Info("[db] dbLoadWorkflow failed: %s", err)
					continue
				}
				t.done <- ctx
			default:
				dsp.log.Info("[db] Invalid DB Action: %T. Value %v", t, t)
			}
		}
	}
}

func (dsp *DatabaseDispatcher) _LoadWorkflow(req *DbLoadWorkflow) (*WorkflowContext, error) {
	db := dsp.db
	log := req.log
	var context WorkflowContext

	context.uuid = req.uuid
	context.done = make(chan *WorkflowContext)

	query := `SELECT id FROM workflow WHERE uuid=?`
	log.DbQuery(query, req.uuid.String())
	row := db.QueryRow(query, req.uuid)
	err := row.Scan(&context.primaryKey)
	if err != nil {
		return nil, err
	}

	return dsp._LoadWorkflowSources(log, &context, context.primaryKey)
}

func (dsp *DatabaseDispatcher) _LoadWorkflowPK(log *Logger, primaryKey int64) (*WorkflowContext, error) {
	db := dsp.db
	var context WorkflowContext
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

func (dsp *DatabaseDispatcher) _LoadWorkflowSources(log *Logger, context *WorkflowContext, primaryKey int64) (*WorkflowContext, error) {
	db := dsp.db
	var sources WorkflowSources

	context.done = make(chan *WorkflowContext)
	context.status = dsp._GetWorkflowStatus(&DbGetStatus{log, context, make(chan string, 1)})

	query := `SELECT wdl, inputs, options FROM workflow_sources WHERE workflow_id=?`
	log.DbQuery(query, strconv.FormatInt(context.primaryKey, 10))
	row := db.QueryRow(query, context.primaryKey)
	err := row.Scan(&sources.wdl, &sources.inputs, &sources.options)
	if err != nil {
		return nil, err
	}
	context.source = &sources

	return context, nil
}

func e(err error) {
	if err != nil {
		panic(err)
	}
}

func contains(a string, list []string) bool {
	for _, b := range list {
		if b == a {
			return true
		}
	}
	return false
}
