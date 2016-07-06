package main

import (
	"database/sql"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"log"
)

func e(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	db, err := sql.Open("sqlite3", "DB")
	if err != nil {
		log.Fatal(err)
	}

	stmt, err := db.Prepare("INSERT INTO t(a, b) values(?,?)")
	e(err)

	res, err := stmt.Exec("one-hundred-one", 101)
	e(err)

	id, err := res.LastInsertId()
	e(err)

	fmt.Println(id)

	rows, err := db.Query("SELECT * FROM t")
	e(err)

	for rows.Next() {
		var a string
		var b int
		err = rows.Scan(&a, &b)
		e(err)
		fmt.Println(a)
		fmt.Println(b)
	}

	defer db.Close()
}
