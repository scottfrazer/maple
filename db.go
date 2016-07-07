package main

import (
	"database/sql"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	//"github.com/satori/go.uuid"
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

var Dbms = "sqlite3"
var DbmsConnectionString = "DB"
var DbActionChannel = make(chan interface{}, 4000)

type DbSetStatus struct {
	wfCtx  *WorkflowContext
	time   Time
	status string
	done   *chan bool
}

type DbGetStatus struct {
	uuid string
	done chan string
}

func DbSetStatusAsync(uuid, status string) chan bool {
	done := make(chan bool, 1)
	DbActionChannel <- DbSetStatus{uuid, status, &done}
	return done
}

func DbGetStatusAsync(uuid string) chan string {
	done := make(chan string, 1)
	DbActionChannel <- DbGetStatus{uuid, done}
	return done
}

func createWorkflowStatusTable(db *sql.DB) {
	var query = `CREATE TABLE workflow_status (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uuid TEXT,
		status TEXT,
		date TEXT
	);`
	fmt.Printf("[db] %s\n", query)
	_, err := db.Exec(query)
	e(err)
}

func setStatus(db *sql.DB, uuid string, status string) {
	var nowISO8601 = time.Now().Format("2006-01-02 15:04:05.999")
	var query = `INSERT INTO workflow_status (uuid, status, date) VALUES (?, ?, ?)`
	fmt.Printf("[db] %s -- [%s, %s, %s]\n", query, uuid, status, nowISO8601)
	_, err := db.Exec(query, uuid, status, nowISO8601)
	e(err)
}

func getStatus(db *sql.DB, uuid string) string {
	var query = `SELECT status FROM workflow_status WHERE uuid=? ORDER BY datetime(date) DESC, id DESC LIMIT 1`
	fmt.Printf("[db] %s -- [%s]\n", query, uuid)
	row := db.QueryRow(query, uuid)
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

	defer func() {
		db.Close()
		isDbDispatcherStarted = false
	}()

	var tableNames = tables(db)
	if !contains("workflow_status", tableNames) {
		createWorkflowStatusTable(db)
	}

	for {
		select {
		case action := <-DbActionChannel:
			switch t := action.(type) {
			case DbSetStatus:
				setStatus(db, t.uuid, t.status)
				if t.done != nil {
					*t.done <- true
				}
			case DbGetStatus:
				t.done <- getStatus(db, t.uuid)
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
