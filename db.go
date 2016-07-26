package main

import (
	"database/sql"
	"errors"
	_ "github.com/mattn/go-sqlite3"
	"github.com/satori/go.uuid"
	"strconv"
	"strings"
	"time"
)

type DatabaseDispatcher struct {
	driverName     string
	dataSourceName string
	log            *Logger
	db             *sql.DB
}

func NewDatabaseDispatcher(driverName, dataSourceName string, log *Logger) *DatabaseDispatcher {
	db, err := sql.Open(driverName, dataSourceName)
	if err != nil {
		panic(err)
	}
	dsp := &DatabaseDispatcher{driverName, dataSourceName, log, db}
	dsp.setup()
	return dsp
}

func (dsp *DatabaseDispatcher) Close() {
	// TODO: close dsp.db
}

func (dsp *DatabaseDispatcher) tables() ([]string, error) {
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

func (dsp *DatabaseDispatcher) query(query string) {
	dsp.log.DbQuery(query)
	_, err := dsp.db.Exec(query)
	if err != nil {
		panic(err)
	}
}

func (dsp *DatabaseDispatcher) setup() {
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
			workflow_id INTEGER NOT NULL,
			job_id INTEGER NOT NULL,
			status TEXT,
			date TEXT,
			FOREIGN KEY(workflow_id) REFERENCES workflow(id)
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

func (dsp *DatabaseDispatcher) NewWorkflow(uuid uuid.UUID, sources *WorkflowSources, log *Logger) (*WorkflowContext, error) {
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

	ctx := WorkflowContext{uuid, workflowId, make(chan *WorkflowContext, 1), sources, "NotStarted", nil}
	success = true
	return &ctx, nil
}

func (dsp *DatabaseDispatcher) LoadWorkflow(uuid uuid.UUID, log *Logger) (*WorkflowContext, error) {
	db := dsp.db
	var context WorkflowContext

	context.uuid = uuid
	context.done = make(chan *WorkflowContext)

	query := `SELECT id FROM workflow WHERE uuid=?`
	log.DbQuery(query, uuid.String())
	row := db.QueryRow(query, uuid)
	err := row.Scan(&context.primaryKey)
	if err != nil {
		return nil, err
	}

	return dsp._LoadWorkflowSources(log, &context, context.primaryKey)
}

func (dsp *DatabaseDispatcher) SetWorkflowStatus(wfId WorkflowIdentifier, status string, log *Logger) (bool, error) {
	db := dsp.db
	var nowISO8601 = time.Now().Format("2006-01-02 15:04:05.999")
	var query = `INSERT INTO workflow_status (workflow_id, status, date) VALUES (?, ?, ?)`
	log.DbQuery(query, strconv.FormatInt(wfId.dbKey(), 10), status, nowISO8601)
	_, err := db.Exec(query, wfId.dbKey(), status, nowISO8601)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (dsp *DatabaseDispatcher) GetWorkflowStatus(wfId WorkflowIdentifier, log *Logger) (string, error) {
	db := dsp.db
	var query = `SELECT status FROM workflow_status WHERE workflow_id=? ORDER BY datetime(date) DESC, id DESC LIMIT 1`
	log.DbQuery(query, strconv.FormatInt(wfId.dbKey(), 10))
	row := db.QueryRow(query, wfId.dbKey())
	var status string
	err := row.Scan(&status)
	if err != nil {
		return "", err
	}
	return status, nil
}

func (dsp *DatabaseDispatcher) GetWorkflowsByStatus(log *Logger, status ...string) ([]*WorkflowContext, error) {
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

	var contexts []*WorkflowContext
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

func (dsp *DatabaseDispatcher) _GetWorkflowStatus(log *Logger, wfId WorkflowIdentifier) (string, error) {
	db := dsp.db
	var query = `SELECT status FROM workflow_status WHERE workflow_id=? ORDER BY datetime(date) DESC, id DESC LIMIT 1`
	log.DbQuery(query, strconv.FormatInt(wfId.dbKey(), 10))
	row := db.QueryRow(query, wfId.dbKey())
	var status string
	err := row.Scan(&status)
	if err != nil {
		return "", err
	}
	return status, nil
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
	var err error

	context.done = make(chan *WorkflowContext)
	context.status, err = dsp._GetWorkflowStatus(log, context)
	if err != nil {
		return nil, err
	}

	query := `SELECT wdl, inputs, options FROM workflow_sources WHERE workflow_id=?`
	log.DbQuery(query, strconv.FormatInt(context.primaryKey, 10))
	row := db.QueryRow(query, context.primaryKey)
	err = row.Scan(&sources.wdl, &sources.inputs, &sources.options)
	if err != nil {
		return nil, err
	}
	context.source = &sources

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
