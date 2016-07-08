package main

import (
	"database/sql"
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

type DbSubmitWorkflow struct {
	uuid    uuid.UUID
	sources *WorkflowSources
	done    chan int64
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

func DbCreateWorkflowAsync(uuid uuid.UUID, sources *WorkflowSources) chan int64 {
	done := make(chan int64, 1)
	DbActionChannel <- DbSubmitWorkflow{uuid, sources, done}
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

func dbSubmitWorkflow(db *sql.DB, uuid uuid.UUID, sources *WorkflowSources) int64 {
	var success = true
	var workflowId int64 = -1

	tx, err := db.Begin()
	e(err)

	defer func() {
		if success {
			tx.Commit()
		} else {
			tx.Rollback()
		}
		e(err)
	}()

	res, err := tx.Exec(`INSERT INTO workflow (uuid) VALUES (?)`, uuid)
	e(err)

	workflowId, err = res.LastInsertId()
	e(err)

	rows, err := res.RowsAffected()
	e(err)

	if rows != 1 {
		panic("could not insert into 'workflow' table")
		success = false
		return -1
	}

	res, err = tx.Exec(`INSERT INTO workflow_sources (workflow_id, wdl, inputs, options) VALUES (?, ?, ?, ?)`, workflowId, sources.wdl, sources.inputs, sources.options)
	e(err)

	rows, err = res.RowsAffected()
	e(err)

	if rows != 1 {
		panic("could not insert into 'workflow_sources' table")
		success = false
		return -1
	}

	res, err = tx.Exec(`INSERT INTO workflow_status (workflow_id, status, date) VALUES (?, 'NotStarted', ?)`, workflowId, time.Now().Format("2006-01-02 15:04:05.999"))
	e(err)

	rows, err = res.RowsAffected()
	e(err)

	if rows != 1 {
		panic("could not insert into 'workflow_status' table")
		success = false
		return -1
	}

	return workflowId
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
	fmt.Printf("[db] %s -- [%s]\n", query, wfId.dbKey())
	row := db.QueryRow(query, wfId.dbKey())
	var status string
	err := row.Scan(&status)
	e(err)
	return status
}

var isDbDispatcherStarted = false

func dbDispatcher() {
	db, err := sql.Open("sqlite3", "DB")
	if err != nil {
		log.Fatal(err)
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
			case DbSubmitWorkflow:
				t.done <- dbSubmitWorkflow(db, t.uuid, t.sources)
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

/*func main() {
	db, err := sql.Open("sqlite3", "DB")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	go dbDispatcher(db)

	for i := 0; i < 0; i++ {
		DbActionChannel <- DbSetStatus{fmt.Sprintf("uuid-%d", i), "sts", nil}
	}

	var id = uuid.NewV4()
	DbActionChannel <- DbSetStatus{fmt.Sprintf("%s", id), "Started", nil}
	time.Sleep(time.Second)
	DbActionChannel <- DbSetStatus{fmt.Sprintf("%s", id), "Doing Stuff", nil}
	time.Sleep(time.Second)
	DbActionChannel <- DbSetStatus{fmt.Sprintf("%s", id), "Done-ish", nil}
	var done = make(chan string)
	DbActionChannel <- DbGetStatus{fmt.Sprintf("%s", id), done}
	fmt.Println(<-done)
}*/
