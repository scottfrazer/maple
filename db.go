package main

import (
	"database/sql"
	"errors"
	_ "github.com/mattn/go-sqlite3"
	"github.com/satori/go.uuid"
	"strings"
	"time"
)

var Dbms = "sqlite3"
var DbmsConnectionString = "DB"
var DbActionChannel = make(chan interface{})
var isDbDispatcherStarted = false

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

func tables(log *Logger, db *sql.DB) []string {
	query := "SELECT name FROM sqlite_master WHERE type='table';"
	log.Info(query)
	rows, err := db.Query(query)
	e(err)
	defer rows.Close()

	var tables = make([]string, 20)
	var i = 0
	for rows.Next() {
		var name string
		err = rows.Scan(&name)
		e(err)
		tables[i] = name
		i++
	}

	err = rows.Err()
	e(err)

	return tables
}

func query(log *Logger, db *sql.DB, query string) {
	log.Info("[db] %s", query)
	_, err := db.Exec(query)
	e(err)
}

func setup(log *Logger, db *sql.DB) {
	var tableNames = tables(log, db)

	if !contains("workflow", tableNames) {
		query(log, db, `CREATE TABLE workflow (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			uuid TEXT
		);`)
	}

	if !contains("workflow_status", tableNames) {
		query(log, db, `CREATE TABLE workflow_status (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			workflow_id INTEGER NOT NULL,
			status TEXT,
			date TEXT,
			FOREIGN KEY(workflow_id) REFERENCES workflow(id)
		);`)
	}

	if !contains("workflow_sources", tableNames) {
		query(log, db, `CREATE TABLE workflow_sources (
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

func DbNewWorkflowAsync(uuid uuid.UUID, sources *WorkflowSources, log *Logger) chan *WorkflowContext {
	done := make(chan *WorkflowContext, 1)
	DbActionChannel <- DbNewWorkflow{log, uuid, sources, done}
	return done
}

func DbLoadWorkflowAsync(uuid uuid.UUID, log *Logger) chan *WorkflowContext {
	done := make(chan *WorkflowContext, 1)
	DbActionChannel <- DbLoadWorkflow{log, uuid, done}
	return done
}

func DbSetStatusAsync(wfId WorkflowIdentifier, status string, log *Logger) chan bool {
	done := make(chan bool, 1)
	DbActionChannel <- DbSetStatus{log, wfId, time.Now(), status, &done}
	return done
}

func DbGetStatusAsync(wfId WorkflowIdentifier, log *Logger) chan string {
	done := make(chan string, 1)
	DbActionChannel <- DbGetStatus{log, wfId, done}
	return done
}

func DbGetByStatusAsync(log *Logger, status ...string) chan []*WorkflowContext {
	done := make(chan []*WorkflowContext, 1)
	DbActionChannel <- DbGetByStatus{log, status, done}
	return done
}

func dbLoadWorkflowFromUUID(db *sql.DB, req *DbLoadWorkflow) (*WorkflowContext, error) {
	log := req.log
	var context WorkflowContext

	context.uuid = req.uuid
	context.done = make(chan *WorkflowContext)

	query := `SELECT id FROM workflow WHERE uuid=?`
	log.Info("[db] %s [%s]", query, req.uuid)
	row := db.QueryRow(query, req.uuid)
	err := row.Scan(&context.primaryKey)
	if err != nil {
		return nil, err
	}

	return _dbLoadWorkflowFromPrimaryKey(log, db, &context, context.primaryKey)
}

func dbLoadWorkflowFromPrimaryKey(log *Logger, db *sql.DB, primaryKey int64) (*WorkflowContext, error) {
	var context WorkflowContext
	context.primaryKey = primaryKey

	query := `SELECT uuid FROM workflow WHERE id=?`
	log.Info("[db] %s [%d]", query, primaryKey)
	row := db.QueryRow(query, primaryKey)
	err := row.Scan(&context.uuid)
	if err != nil {
		return nil, err
	}

	return _dbLoadWorkflowFromPrimaryKey(log, db, &context, primaryKey)
}

func _dbLoadWorkflowFromPrimaryKey(log *Logger, db *sql.DB, context *WorkflowContext, primaryKey int64) (*WorkflowContext, error) {
	var sources WorkflowSources

	context.done = make(chan *WorkflowContext)
	context.status = dbGetStatus(db, &DbGetStatus{log, context, make(chan string, 1)})

	query := `SELECT wdl, inputs, options FROM workflow_sources WHERE workflow_id=?`
	log.Info("[db] %s [%d]", query, context.primaryKey)
	row := db.QueryRow(query, context.primaryKey)
	err := row.Scan(&sources.wdl, &sources.inputs, &sources.options)
	if err != nil {
		return nil, err
	}
	context.source = &sources

	return context, nil
}

func dbNewWorkflow(db *sql.DB, req *DbNewWorkflow) (*WorkflowContext, error) {
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
	log.Info("[db] %s :: %s", query, req.uuid)
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
	log.Info("[db] %s", query)
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
	log.Info("[db] %s :: %d, %s", query, workflowId, now)
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

func dbSetStatus(db *sql.DB, req *DbSetStatus) {
	log := req.log
	var nowISO8601 = time.Now().Format("2006-01-02 15:04:05.999")
	var query = `INSERT INTO workflow_status (workflow_id, status, date) VALUES (?, ?, ?)`
	log.Info("[db] %s -- [%d, %s, %s]", query, req.id.dbKey(), req.status, nowISO8601)
	_, err := db.Exec(query, req.id.dbKey(), req.status, nowISO8601)
	e(err)
}

func dbGetStatus(db *sql.DB, req *DbGetStatus) string {
	log := req.log
	var query = `SELECT status FROM workflow_status WHERE workflow_id=? ORDER BY datetime(date) DESC, id DESC LIMIT 1`
	log.Info("[db] %s -- [%d]", query, req.id.dbKey())
	row := db.QueryRow(query, req.id.dbKey())
	var status string
	err := row.Scan(&status)
	e(err)
	return status
}

func dbGetByStatus(db *sql.DB, req *DbGetByStatus) ([]*WorkflowContext, error) {
	log := req.log
	questionMarks := make([]string, len(req.statuses))
	for i := 0; i < len(req.statuses); i++ {
		questionMarks[i] = "?"
	}
	var query = `SELECT workflow_id FROM (SELECT workflow_id, status, MAX(date) FROM workflow_status GROUP BY workflow_id) WHERE status IN (` + strings.Join(questionMarks, ", ") + `);`
	log.Info("[db] %s -- [%s]", query, strings.Join(req.statuses, ", "))

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
		context, err := dbLoadWorkflowFromPrimaryKey(log, db, id)
		e(err)
		contexts = append(contexts, context)
	}

	err = rows.Err()
	if err != nil {
		return nil, err
	}

	return contexts, nil
}

func dbDispatcher(log *Logger) {
	db, err := sql.Open(Dbms, DbmsConnectionString)
	if err != nil {
		panic(err)
	}

	setup(log, db)

	defer func() {
		db.Close()
		isDbDispatcherStarted = false
	}()

	for {
		select {
		case action := <-DbActionChannel:
			switch t := action.(type) {
			case DbSetStatus:
				dbSetStatus(db, &t)
				if t.done != nil {
					*t.done <- true
				}
			case DbGetStatus:
				t.done <- dbGetStatus(db, &t)
			case DbGetByStatus:
				uuids, err := dbGetByStatus(db, &t)
				if err != nil {
					log.Info("dbGetByStatus failed: %s", err)
					continue
				}
				t.done <- uuids
			case DbNewWorkflow:
				ctx, err := dbNewWorkflow(db, &t)
				if err != nil {
					log.Info("dbNewWorkflow failed: %s", err)
					continue
				}
				t.done <- ctx
			case DbLoadWorkflow:
				ctx, err := dbLoadWorkflowFromUUID(db, &t)
				if err != nil {
					log.Info("dbLoadWorkflow failed: %s", err)
					continue
				}
				t.done <- ctx
			default:
				log.Info("[db] Invalid DB Action: %T. Value %v", t, t)
			}
		}
	}
}

func StartDbDispatcher(log *Logger) {
	if !isDbDispatcherStarted {
		go dbDispatcher(log)
		isDbDispatcherStarted = true
	}
}
