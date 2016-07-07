package main

import (
	"database/sql"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
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

	var tables = make([]string, 20)
	var i = 0
	for rows.Next() {
		var name string
		err = rows.Scan(&name)
		e(err)
		tables[i] = name
		i++
	}

	return tables
}

type DbStatusUpdate struct {
	uuid   string
	status string
	done   *chan bool
}

type DbGetStatus struct {
	uuid string
	done chan string
}

var Dbms = "sqlite3"
var DbActionChannel = make(chan interface{}, 4000)

func createWorkflowTable(db *sql.DB) {
	var query = `CREATE TABLE workflow (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uuid TEXT,
		status TEXT
	);`
	_, err := db.Exec(query)
	e(err)
	fmt.Println("Created workflow table")
}

func setStatus(db *sql.DB, uuid string, status string) {
	var query = `INSERT INTO workflow (uuid, status) VALUES (?, ?)`
	_, err := db.Exec(query, uuid, status)
	e(err)
}

func dbDispatcher(db *sql.DB) {
	var tableNames = tables(db)
	if !contains("workflow", tableNames) {
		createWorkflowTable(db)
	}

	for {
		select {
		case action := <-DbActionChannel:
			switch t := action.(type) {
			case DbStatusUpdate:
				fmt.Printf("[db] SET STATUS %s %s\n", t.uuid, t.status)
				setStatus(db, t.uuid, t.status)
				if t.done != nil {
					*t.done <- true
				}
			case DbGetStatus:
				fmt.Printf("[db] GET STATUS %s\n", t.uuid)
			default:
				fmt.Printf("[db] Invalid DB Action: %T. Value %v\n", t, t)
			}
		}
	}
}

func main() {
	db, err := sql.Open("sqlite3", "DB")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	go dbDispatcher(db)

	for i := 0; i < 100; i++ {
		DbActionChannel <- DbStatusUpdate{"id", "sts", nil}
	}

	time.Sleep(time.Second * 15)
	/*stmt, err := db.Prepare("INSERT INTO t(a, b) values(?,?)")
	e(err)

	res, err := stmt.Exec("one-hundred-one", 101)
	e(err)

	id, err := res.LastInsertId()
	e(err)

	fmt.Println(id)*/
}
