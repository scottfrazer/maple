package maple

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

// Used to match sql.DB.Exec and sql.Tx.Exec
type dbExecFunc func(string, ...interface{}) (sql.Result, error)

type MapleDb struct {
	driverName     string
	dataSourceName string
	log            *Logger
	db             *sql.DB
	mtx            *sync.Mutex
}

type WorkflowEntry struct {
	primaryKey    int64
	uuid          uuid.UUID
	jobs          []*JobEntry
	statusEntries []*WorkflowStatusEntry
	sources       *WorkflowSourcesEntry
}

func (we *WorkflowEntry) LatestStatusEntry() *WorkflowStatusEntry {
	var latest *WorkflowStatusEntry = nil
	for _, entry := range we.statusEntries {
		if latest == nil || entry.date.After(latest.date) {
			latest = entry
		}
		if entry.date.Equal(latest.date) && latest.primaryKey < entry.primaryKey {
			latest = entry
		}
	}
	return latest
}

type WorkflowStatusEntry struct {
	primaryKey int64
	status     string
	date       time.Time
}

type WorkflowSourcesEntry struct {
	primaryKey int64
	wdl        string
	inputs     string
	options    string
}

type JobEntry struct {
	primaryKey    int64
	fqn           string
	shard         int
	attempt       int
	statusEntries []*JobStatusEntry
}

func (je *JobEntry) LatestStatusEntry() *JobStatusEntry {
	var latest *JobStatusEntry = nil
	for _, entry := range je.statusEntries {
		if latest == nil || entry.date.After(latest.date) {
			latest = entry
		}
		if entry.date.Equal(latest.date) && latest.primaryKey < entry.primaryKey {
			latest = entry
		}
	}
	return latest
}

type JobStatusEntry struct {
	primaryKey int64
	status     string
	date       time.Time
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

func (dsp *MapleDb) NewJobEntry(workflowPrimaryKey int64, fqn string, shard, attempt int, log *Logger) (*JobEntry, error) {
	dsp.mtx.Lock()
	defer dsp.mtx.Unlock()
	db := dsp.db
	var success = false
	var primaryKey int64 = -1

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
	// TODO: DbQuery really needs to be able to take integers as parameters
	log.DbQuery(query, strconv.FormatInt(workflowPrimaryKey, 10), fqn, strconv.FormatInt(int64(shard), 10), strconv.FormatInt(int64(attempt), 10))
	res, err := tx.Exec(query, workflowPrimaryKey, fqn, shard, attempt)
	if err != nil {
		return nil, err
	}

	primaryKey, err = res.LastInsertId()
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

	statusEntry, err := dsp.newJobStatusEntry(primaryKey, "NotStarted", time.Now(), log, tx.Exec)
	if err != nil {
		return nil, err
	}

	success = true

	var entry JobEntry
	entry.primaryKey = primaryKey
	entry.fqn = fqn
	entry.shard = shard
	entry.attempt = attempt
	entry.statusEntries = make([]*JobStatusEntry, 1)
	entry.statusEntries[0] = statusEntry
	return &entry, nil
}

func (dsp *MapleDb) NewJobStatusEntry(jobPrimaryKey int64, status string, date time.Time, log *Logger) (*JobStatusEntry, error) {
	dsp.mtx.Lock()
	defer dsp.mtx.Unlock()
	return dsp.newJobStatusEntry(jobPrimaryKey, status, date, log, dsp.db.Exec)
}

func (dsp *MapleDb) newJobStatusEntry(jobPrimaryKey int64, status string, date time.Time, log *Logger, exec dbExecFunc) (*JobStatusEntry, error) {
	date8601 := date.Format("2006-01-02 15:04:05.999")
	query := `INSERT INTO job_status (job_id, status, date) VALUES (?, ?, ?)`
	log.DbQuery(query, strconv.FormatInt(jobPrimaryKey, 10), status, date8601)
	res, err := exec(query, jobPrimaryKey, status, date8601)
	if err != nil {
		return nil, err
	}

	primaryKey, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}

	if rows != 1 {
		return nil, errors.New("could not insert into 'job_status' table")
	}

	var entry JobStatusEntry
	entry.primaryKey = primaryKey
	entry.status = status
	entry.date = date
	return &entry, nil
}

func (dsp *MapleDb) NewWorkflowEntry(uuid uuid.UUID, wdl, inputs, options string, log *Logger) (*WorkflowEntry, error) {
	dsp.mtx.Lock()
	defer dsp.mtx.Unlock()
	db := dsp.db
	var success = false
	var primaryKey int64 = -1

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

	primaryKey, err = res.LastInsertId()
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

	sources, err := dsp.newWorkflowSourcesEntry(primaryKey, wdl, inputs, options, log, tx.Exec)
	if err != nil {
		return nil, err
	}

	statusEntry, err := dsp.newWorkflowStatusEntry(primaryKey, "NotStarted", time.Now(), log, tx.Exec)
	if err != nil {
		return nil, err
	}

	success = true

	var entry WorkflowEntry
	entry.primaryKey = primaryKey
	entry.uuid = uuid
	entry.statusEntries = make([]*WorkflowStatusEntry, 1)
	entry.statusEntries[0] = statusEntry
	entry.sources = sources
	return &entry, nil
}

func (dsp *MapleDb) newWorkflowSourcesEntry(workflowPrimaryKey int64, wdl, inputs, options string, log *Logger, exec dbExecFunc) (*WorkflowSourcesEntry, error) {
	query := `INSERT INTO workflow_sources (workflow_id, wdl, inputs, options) VALUES (?, ?, ?, ?)`
	log.DbQuery(query, strconv.FormatInt(workflowPrimaryKey, 10), "{omit}", "{omit}", "{omit}")
	res, err := exec(query, workflowPrimaryKey, wdl, inputs, options)
	if err != nil {
		return nil, err
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}

	if rows != 1 {
		return nil, errors.New("could not insert into 'workflow_sources' table")
	}

	var entry WorkflowSourcesEntry
	entry.wdl = wdl
	entry.inputs = inputs
	entry.options = options
	return &entry, nil
}

func (dsp *MapleDb) newWorkflowStatusEntry(workflowPrimaryKey int64, status string, date time.Time, log *Logger, exec dbExecFunc) (*WorkflowStatusEntry, error) {
	date8601 := date.Format("2006-01-02 15:04:05.999")
	query := `INSERT INTO workflow_status (workflow_id, status, date) VALUES (?, ?, ?)`
	log.DbQuery(query, strconv.FormatInt(workflowPrimaryKey, 10), status, date8601)
	res, err := exec(query, workflowPrimaryKey, status, date8601)
	if err != nil {
		return nil, err
	}

	primaryKey, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}

	if rows != 1 {
		return nil, errors.New("could not insert into 'workflow_status' table")
	}

	var entry WorkflowStatusEntry
	entry.primaryKey = primaryKey
	entry.status = status
	entry.date = date
	return &entry, nil
}

func (dsp *MapleDb) LoadWorkflow(uuid uuid.UUID, log *Logger) (*WorkflowEntry, error) {
	dsp.mtx.Lock()
	defer dsp.mtx.Unlock()
	var primaryKey int64

	query := `SELECT id FROM workflow WHERE uuid=?`
	log.DbQuery(query, uuid.String())
	row := dsp.db.QueryRow(query, uuid)
	err := row.Scan(&primaryKey)
	if err != nil {
		return nil, err
	}

	entry, err := dsp.loadWorkflowEntry(log, primaryKey)
	if err != nil {
		return nil, err
	}
	return entry, nil
}

func (dsp *MapleDb) NewWorkflowStatusEntry(primaryKey int64, status string, date time.Time, log *Logger) (*WorkflowStatusEntry, error) {
	dsp.mtx.Lock()
	defer dsp.mtx.Unlock()
	statusEntry, err := dsp.newWorkflowStatusEntry(primaryKey, status, time.Now(), log, dsp.db.Exec)
	if err != nil {
		return nil, err
	}
	return statusEntry, nil
}

func (dsp *MapleDb) LoadWorkflowsByStatus(log *Logger, status ...string) ([]*WorkflowEntry, error) {
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

	var entries []*WorkflowEntry
	for rows.Next() {
		var id int64
		err = rows.Scan(&id)
		if err != nil {
			return nil, err
		}
		entry, err := dsp.loadWorkflowEntry(log, id)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}

	err = rows.Err()
	if err != nil {
		return nil, err
	}

	return entries, nil
}

func (dsp *MapleDb) loadJobStatusEntries(jobPrimaryKey int64, log *Logger) ([]*JobStatusEntry, error) {
	var query = `SELECT id, status, date FROM job_status WHERE job_id=?`
	log.DbQuery(query, strconv.FormatInt(jobPrimaryKey, 10))
	rows, err := dsp.db.Query(query, jobPrimaryKey)
	if err != nil {
		return nil, err
	}

	var entries []*JobStatusEntry
	for rows.Next() {
		var entry JobStatusEntry
		var date string

		err = rows.Scan(&entry.primaryKey, &entry.status, &date)

		if err != nil {
			return nil, err
		}

		entry.date, err = time.Parse("2006-01-02 15:04:05.999", date)

		if err != nil {
			return nil, err
		}

		entries = append(entries, &entry)
	}
	return entries, nil
}

func (dsp *MapleDb) loadWorkflowStatusEntries(workflowPrimaryKey int64, log *Logger) ([]*WorkflowStatusEntry, error) {
	var query = `SELECT id, status, date FROM workflow_status WHERE workflow_id=?`
	log.DbQuery(query, strconv.FormatInt(workflowPrimaryKey, 10))
	rows, err := dsp.db.Query(query, workflowPrimaryKey)
	if err != nil {
		return nil, err
	}

	var entries []*WorkflowStatusEntry
	for rows.Next() {
		var entry WorkflowStatusEntry
		var date string

		err = rows.Scan(&entry.primaryKey, &entry.status, &date)

		if err != nil {
			return nil, err
		}

		entry.date, err = time.Parse("2006-01-02 15:04:05.999", date)

		if err != nil {
			return nil, err
		}

		entries = append(entries, &entry)
	}
	return entries, nil
}

func (dsp *MapleDb) loadWorkflowEntry(log *Logger, primaryKey int64) (*WorkflowEntry, error) {
	var entry WorkflowEntry
	entry.primaryKey = primaryKey

	query := `SELECT uuid FROM workflow WHERE id=?`
	log.DbQuery(query, strconv.FormatInt(primaryKey, 10))
	row := dsp.db.QueryRow(query, primaryKey)
	err := row.Scan(&entry.uuid)
	if err != nil {
		return nil, err
	}

	sources, err := dsp.loadWorkflowSourcesEntry(log, primaryKey)
	if err != nil {
		return nil, err
	}
	entry.sources = sources

	statusEntries, err := dsp.loadWorkflowStatusEntries(primaryKey, log)
	if err != nil {
		return nil, err
	}
	entry.statusEntries = statusEntries

	jobs, err := dsp.loadJobEntries(log, primaryKey)
	if err != nil {
		return nil, err
	}
	entry.jobs = jobs

	return &entry, nil
}

func (dsp *MapleDb) loadWorkflowSourcesEntry(log *Logger, workflowPrimaryKey int64) (*WorkflowSourcesEntry, error) {
	var entry WorkflowSourcesEntry

	query := `SELECT wdl, inputs, options FROM workflow_sources WHERE workflow_id=?`
	log.DbQuery(query, strconv.FormatInt(workflowPrimaryKey, 10))
	row := dsp.db.QueryRow(query, workflowPrimaryKey)
	err := row.Scan(&entry.wdl, &entry.inputs, &entry.options)
	if err != nil {
		return nil, err
	}

	return &entry, nil
}

func (dsp *MapleDb) loadJobEntries(log *Logger, workflowPrimaryKey int64) ([]*JobEntry, error) {
	query := `SELECT id, call_fqn, shard, attempt FROM job WHERE workflow_id=?`
	log.DbQuery(query, strconv.FormatInt(workflowPrimaryKey, 10))
	rows, err := dsp.db.Query(query, workflowPrimaryKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*JobEntry
	for rows.Next() {
		var entry JobEntry

		err = rows.Scan(&entry.primaryKey, &entry.fqn, &entry.shard, &entry.attempt)
		if err != nil {
			return nil, err
		}

		statusEntries, err := dsp.loadJobStatusEntries(entry.primaryKey, log)
		if err != nil {
			return nil, err
		}

		entry.statusEntries = statusEntries

		entries = append(entries, &entry)
	}

	return entries, nil
}

func contains(a string, list []string) bool {
	for _, b := range list {
		if b == a {
			return true
		}
	}
	return false
}
