package main

import (
	"database/sql"
	"errors"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"github.com/satori/go.uuid"
	"log"
	"time"
)

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

func tables(db *sql.DB) []string {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table';")
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
	if err != nil {
		log.Fatal(err)
	}

	return tables
}

func query(db *sql.DB, query string) {
	fmt.Printf("[db] %s\n", query)
	_, err := db.Exec(query)
	e(err)
}

func setup(db *sql.DB) {
	var tableNames = tables(db)

	if !contains("workflow", tableNames) {
		query(db, `CREATE TABLE workflow (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			uuid TEXT
		);`)
	}

	if !contains("workflow_status", tableNames) {
		query(db, `CREATE TABLE workflow_status (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			workflow_id INTEGER NOT NULL,
			status TEXT,
			date TEXT,
			FOREIGN KEY(workflow_id) REFERENCES workflow(id)
		);`)
	}

	if !contains("workflow_sources", tableNames) {
		query(db, `CREATE TABLE workflow_sources (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			workflow_id INTEGER NOT NULL,
			wdl TEXT,
			inputs TEXT,
			options TEXT,
			FOREIGN KEY(workflow_id) REFERENCES workflow(id)
		);`)
	}
}

var Dbms = "sqlite3"
var DbmsConnectionString = "DB"
var DbActionChannel = make(chan interface{}, 4000)

type DbNewWorkflow struct {
	uuid    uuid.UUID
	sources *WorkflowSources
	done    chan *WorkflowContext
}

type DbLoadWorkflow struct {
	uuid uuid.UUID
	done chan *WorkflowContext
}

type DbSetStatus struct {
	wfId   WorkflowIdentifier
	time   time.Time
	status string
	done   *chan bool
}

type DbGetStatus struct {
	wfId WorkflowIdentifier
	done chan string
}

type DbGetByStatus struct {
	status string
	done   chan []*WorkflowContext
}

func DbNewWorkflowAsync(uuid uuid.UUID, sources *WorkflowSources) chan *WorkflowContext {
	done := make(chan *WorkflowContext, 1)
	DbActionChannel <- DbNewWorkflow{uuid, sources, done}
	return done
}

func DbLoadWorkflowAsync(uuid uuid.UUID) chan *WorkflowContext {
	done := make(chan *WorkflowContext, 1)
	DbActionChannel <- DbLoadWorkflow{uuid, done}
	return done
}

func DbSetStatusAsync(wfId WorkflowIdentifier, status string) chan bool {
	done := make(chan bool, 1)
	DbActionChannel <- DbSetStatus{wfId, time.Now(), status, &done}
	return done
}

func DbGetStatusAsync(wfId WorkflowIdentifier) chan string {
	done := make(chan string, 1)
	DbActionChannel <- DbGetStatus{wfId, done}
	return done
}

func DbGetByStatusAsync(status string) chan []*WorkflowContext {
	done := make(chan []*WorkflowContext, 1)
	DbActionChannel <- DbGetByStatus{status, done}
	return done
}

func dbLoadWorkflowFromUUID(db *sql.DB, uuid uuid.UUID) (*WorkflowContext, error) {
	var context WorkflowContext
	var sources WorkflowSources

	context.uuid = uuid
	context.done = make(chan *WorkflowContext)
	context.source = &sources

	query := `SELECT id FROM workflow WHERE uuid=?`
	fmt.Printf("[db] %s [%s]\n", query, uuid)
	row := db.QueryRow(query, uuid)
	err := row.Scan(&context.primaryKey)
	if err != nil {
		return nil, err
	}

	return _dbLoadWorkflowFromPrimaryKey(db, &context, context.primaryKey)
}

func dbLoadWorkflowFromPrimaryKey(db *sql.DB, primaryKey int64) (*WorkflowContext, error) {
	var context WorkflowContext
	context.primaryKey = primaryKey

	query := `SELECT uuid FROM workflow WHERE id=?`
	fmt.Printf("[db] %s [%s]\n", query, primaryKey)
	row := db.QueryRow(query, primaryKey)
	err := row.Scan(&context.uuid)
	if err != nil {
		return nil, err
	}

	return _dbLoadWorkflowFromPrimaryKey(db, &context, primaryKey)
}

func _dbLoadWorkflowFromPrimaryKey(db *sql.DB, context *WorkflowContext, primaryKey int64) (*WorkflowContext, error) {
	var sources WorkflowSources

	context.done = make(chan *WorkflowContext)
	context.status = dbGetStatus(db, context)

	query := `SELECT wdl, inputs, options FROM workflow_sources WHERE workflow_id=?`
	fmt.Printf("[db] %s [%d]\n", query, context.primaryKey)
	row := db.QueryRow(query, context.primaryKey)
	err := row.Scan(&sources.wdl, &sources.inputs, &sources.options)
	if err != nil {
		return nil, err
	}
	context.source = &sources

	return context, nil
}

func dbNewWorkflow(db *sql.DB, uuid uuid.UUID, sources *WorkflowSources) (*WorkflowContext, error) {
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

	res, err := tx.Exec(`INSERT INTO workflow (uuid) VALUES (?)`, uuid)
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

	res, err = tx.Exec(`INSERT INTO workflow_sources (workflow_id, wdl, inputs, options) VALUES (?, ?, ?, ?)`, workflowId, sources.wdl, sources.inputs, sources.options)
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

	res, err = tx.Exec(`INSERT INTO workflow_status (workflow_id, status, date) VALUES (?, 'NotStarted', ?)`, workflowId, time.Now().Format("2006-01-02 15:04:05.999"))
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

	ctx := WorkflowContext{uuid, workflowId, make(chan *WorkflowContext, 1), sources, "NotStarted", nil}
	success = true
	return &ctx, nil
}

func dbSetStatus(db *sql.DB, obj DbSetStatus) {
	var nowISO8601 = time.Now().Format("2006-01-02 15:04:05.999")
	var query = `INSERT INTO workflow_status (workflow_id, status, date) VALUES (?, ?, ?)`
	fmt.Printf("[db] %s -- [%d, %s, %s]\n", query, obj.wfId.dbKey(), obj.status, nowISO8601)
	_, err := db.Exec(query, obj.wfId.dbKey(), obj.status, nowISO8601)
	e(err)
}

func dbGetStatus(db *sql.DB, wfId WorkflowIdentifier) string {
	var query = `SELECT status FROM workflow_status WHERE workflow_id=? ORDER BY datetime(date) DESC, id DESC LIMIT 1`
	fmt.Printf("[db] %s -- [%d]\n", query, wfId.dbKey())
	row := db.QueryRow(query, wfId.dbKey())
	var status string
	err := row.Scan(&status)
	e(err)
	return status
}

func dbGetByStatus(db *sql.DB, status string) ([]*WorkflowContext, error) {
	var query = `SELECT workflow_id FROM (SELECT workflow_id, status, MAX(date) FROM workflow_status GROUP BY workflow_id) WHERE status=?;`
	fmt.Printf("[db] %s -- [%s]\n", query, status)
	rows, err := db.Query(query, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contexts []*WorkflowContext
	for rows.Next() {
		var id int64
		err = rows.Scan(&id)
		e(err)
		context, err := dbLoadWorkflowFromPrimaryKey(db, id)
		e(err)
		contexts = append(contexts, context)
	}

	err = rows.Err()
	if err != nil {
		return nil, err
	}

	return contexts, nil
}

var isDbDispatcherStarted = false

func dbDispatcher() {
	db, err := sql.Open(Dbms, DbmsConnectionString)
	if err != nil {
		panic(err)
	}

	setup(db)

	defer func() {
		db.Close()
		isDbDispatcherStarted = false
	}()

	for {
		select {
		case action := <-DbActionChannel:
			switch t := action.(type) {
			case DbSetStatus:
				dbSetStatus(db, t)
				if t.done != nil {
					*t.done <- true
				}
			case DbGetStatus:
				t.done <- dbGetStatus(db, t.wfId)
			case DbGetByStatus:
				uuids, err := dbGetByStatus(db, t.status)
				if err != nil {
					fmt.Printf("dbGetByStatus failed: %s", err)
					continue
				}
				t.done <- uuids
			case DbNewWorkflow:
				ctx, err := dbNewWorkflow(db, t.uuid, t.sources)
				if err != nil {
					fmt.Printf("dbNewWorkflow failed: %s", err)
					continue
				}
				t.done <- ctx
			case DbLoadWorkflow:
				fmt.Printf("DbLoadWorkflow %s\n", t.uuid)
				ctx, err := dbLoadWorkflowFromUUID(db, t.uuid)
				if err != nil {
					fmt.Printf("dbLoadWorkflow failed: %s", err)
					continue
				}
				t.done <- ctx
			default:
				fmt.Printf("[db] Invalid DB Action: %T. Value %v\n", t, t)
			}
		}
	}
}

func StartDbDispatcher() {
	if !isDbDispatcherStarted {
		go dbDispatcher()
		isDbDispatcherStarted = true
	}
}
