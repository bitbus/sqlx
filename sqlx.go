package sqlx

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"

	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/bitbus/sqlx/reflectx"
)

// Although the NameMapper is convenient, in practice it should not
// be relied on except for application code.  If you are writing a library
// that uses sqlx, you should be aware that the name mappings you expect
// can be overridden by your user's application.

// NameMapper is used to map column names to struct field names.  By default,
// it uses strings.ToLower to lowercase struct field names.  It can be set
// to whatever you want, but it is encouraged to be set before sqlx is used
// as name-to-field mappings are cached after first use on a type.
var NameMapper = strings.ToLower
var origMapper = reflect.ValueOf(NameMapper)

// Rather than creating on init, this is created when necessary so that
// importers have time to customize the NameMapper.
var mpr *reflectx.Mapper

// mprMu protects mpr.
var mprMu sync.Mutex

// mapper returns a valid mapper using the configured NameMapper func.
func mapper() *reflectx.Mapper {
	mprMu.Lock()
	defer mprMu.Unlock()

	if mpr == nil {
		mpr = reflectx.NewMapperFunc("db", NameMapper)
	} else if origMapper != reflect.ValueOf(NameMapper) {
		// if NameMapper has changed, create a new mapper
		mpr = reflectx.NewMapperFunc("db", NameMapper)
		origMapper = reflect.ValueOf(NameMapper)
	}
	return mpr
}

// isScannable takes the reflect.Type and the actual dest value and returns
// whether or not it's Scannable.  Something is scannable if:
//   - it is not a struct
//   - it implements sql.Scanner
//   - it has no exported fields
func isScannable(t reflect.Type) bool {
	if reflect.PtrTo(t).Implements(_scannerInterface) {
		return true
	}
	if t.Kind() != reflect.Struct {
		return true
	}

	// it's not important that we use the right mapper for this particular object,
	// we're only concerned on how many exported fields this struct has
	return len(mapper().TypeMap(t).Index) == 0
}

// ColScanner is an interface used by MapScan and SliceScan
type ColScanner interface {
	Columns() ([]string, error)
	Scan(dest ...any) error
	Err() error
}

// Queryer is an interface used by Get and Select
type Queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
	Queryx(query string, args ...any) (*Rows, error)
	QueryRowx(query string, args ...any) *Row
}

// Execer is an interface used by MustExec and LoadFile
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// QueryIn is an interface used by InGet and InSelect
type QueryIn interface {
	Queryer
	In(query string, args ...any) (string, []any, error)
}

// ExecIn is an interface used by MustInExec and InExec
type ExecIn interface {
	Execer
	In(query string, args ...any) (string, []any, error)
}

// Binder is an interface for something which can bind queries (Tx, DB)
type binder interface {
	DriverName() string
	Rebind(string) string
	BindNamed(string, any) (string, []any, error)
}

// Ext is a union interface which can bind, query, and exec, used by
// NamedQuery and NamedExec.
type Ext interface {
	binder
	Queryer
	Execer
}

// Preparer is an interface used by Preparex.
type Preparer interface {
	Prepare(query string) (*sql.Stmt, error)
}

// determine if any of our extensions are unsafe
func isUnsafe(i any) bool {
	switch v := i.(type) {
	case Row:
		return v.unsafe
	case *Row:
		return v.unsafe
	case Rows:
		return v.unsafe
	case *Rows:
		return v.unsafe
	case NamedStmt:
		return v.Stmt.unsafe
	case *NamedStmt:
		return v.Stmt.unsafe
	case Stmt:
		return v.unsafe
	case *Stmt:
		return v.unsafe
	case qStmt:
		return v.unsafe
	case *qStmt:
		return v.unsafe
	case DB:
		return v.unsafe
	case *DB:
		return v.unsafe
	case Tx:
		return v.unsafe
	case *Tx:
		return v.unsafe
	case sql.Rows, *sql.Rows:
		return false
	default:
		return false
	}
}

func mapperFor(i any) *reflectx.Mapper {
	switch i := i.(type) {
	case DB:
		return i.Mapper
	case *DB:
		return i.Mapper
	case Tx:
		return i.Mapper
	case *Tx:
		return i.Mapper
	default:
		return mapper()
	}
}

var _scannerInterface = reflect.TypeOf((*sql.Scanner)(nil)).Elem()
var _valuerInterface = reflect.TypeOf((*driver.Valuer)(nil)).Elem()

// Row is a reimplementation of sql.Row in order to gain access to the underlying
// sql.Rows.Columns() data, necessary for StructScan.
type Row struct {
	err    error
	unsafe bool
	rows   *sql.Rows
	Mapper *reflectx.Mapper
}

// Scan is a fixed implementation of sql.Row.Scan, which does not discard the
// underlying error from the internal rows object if it exists.
func (r *Row) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}

	// TODO(bradfitz): for now we need to defensively clone all
	// []byte that the driver returned (not permitting
	// *RawBytes in Rows.Scan), since we're about to close
	// the Rows in our defer, when we return from this function.
	// the contract with the driver.Next(...) interface is that it
	// can return slices into read-only temporary memory that's
	// only valid until the next Scan/Close.  But the TODO is that
	// for a lot of drivers, this copy will be unnecessary.  We
	// should provide an optional interface for drivers to
	// implement to say, "don't worry, the []bytes that I return
	// from Next will not be modified again." (for instance, if
	// they were obtained from the network anyway) But for now we
	// don't care.
	defer r.rows.Close()
	for _, dp := range dest {
		if _, ok := dp.(*sql.RawBytes); ok {
			return errors.New("sql: RawBytes isn't allowed on Row.Scan")
		}
	}

	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	err := r.rows.Scan(dest...)
	if err != nil {
		return err
	}
	// Make sure the query can be processed to completion with no errors.
	if err := r.rows.Close(); err != nil {
		return err
	}
	return nil
}

// Columns returns the underlying sql.Rows.Columns(), or the deferred error usually
// returned by Row.Scan()
func (r *Row) Columns() ([]string, error) {
	if r.err != nil {
		return []string{}, r.err
	}
	return r.rows.Columns()
}

// ColumnTypes returns the underlying sql.Rows.ColumnTypes(), or the deferred error
func (r *Row) ColumnTypes() ([]*sql.ColumnType, error) {
	if r.err != nil {
		return []*sql.ColumnType{}, r.err
	}
	return r.rows.ColumnTypes()
}

// Err returns the error encountered while scanning.
func (r *Row) Err() error {
	return r.err
}

// DB is a wrapper around sql.DB which keeps track of the driverName upon Open,
// used mostly to automatically bind named queries using the right bindvars.
type DB struct {
	*sql.DB
	driverName string
	unsafe     bool
	Mapper     *reflectx.Mapper
}

// NewDb returns a new sqlx DB wrapper for a pre-existing *sql.DB.  The
// driverName of the original database is required for named query support.
func NewDb(db *sql.DB, driverName string) *DB {
	return &DB{DB: db, driverName: driverName, Mapper: mapper()}
}

// DriverName returns the driverName passed to the Open function for this DB.
func (db *DB) DriverName() string {
	return db.driverName
}

// Open is the same as sql.Open, but returns an *sqlx.DB instead.
func Open(driverName, dataSourceName string) (*DB, error) {
	db, err := sql.Open(driverName, dataSourceName)
	if err != nil {
		return nil, err
	}
	return &DB{DB: db, driverName: driverName, Mapper: mapper()}, err
}

// MustOpen is the same as sql.Open, but returns an *sqlx.DB instead and panics on error.
func MustOpen(driverName, dataSourceName string) *DB {
	db, err := Open(driverName, dataSourceName)
	if err != nil {
		panic(err)
	}
	return db
}

// MapperFunc sets a new mapper for this db using the default sqlx struct tag
// and the provided mapper function.
func (db *DB) MapperFunc(mf func(string) string) {
	db.Mapper = reflectx.NewMapperFunc("db", mf)
}

// Rebind transforms a query from QUESTION to the DB driver's bindvar type.
func (db *DB) Rebind(query string) string {
	return Rebind(BindType(db.driverName), query)
}

// Unsafe returns a version of DB which will silently succeed to scan when
// columns in the SQL result have no fields in the destination struct.
// sqlx.Stmt and sqlx.Tx which are created from this DB will inherit its
// safety behavior.
func (db *DB) Unsafe() *DB {
	return &DB{DB: db.DB, driverName: db.driverName, unsafe: true, Mapper: db.Mapper}
}

// BindNamed binds a query using the DB driver's bindvar type.
func (db *DB) BindNamed(query string, arg any) (string, []any, error) {
	return bindNamedMapper(BindType(db.driverName), query, arg, db.Mapper)
}

// NamedQuery using this DB.
// Any named placeholder parameters are replaced with fields from arg.
func (db *DB) NamedQuery(query string, arg any) (*Rows, error) {
	return NamedQuery(db, query, arg)
}

// NamedExec using this DB.
// Any named placeholder parameters are replaced with fields from arg.
func (db *DB) NamedExec(query string, arg any) (sql.Result, error) {
	return NamedExec(db, query, arg)
}

// Select using this DB.
// Any placeholder parameters are replaced with supplied args.
func (db *DB) Select(dest any, query string, args ...any) error {
	return Select(db, dest, query, args...)
}

// Get using this DB.
// Any placeholder parameters are replaced with supplied args.
// An error is returned if the result set is empty.
func (db *DB) Get(dest any, query string, args ...any) error {
	return Get(db, dest, query, args...)
}

// MustBegin starts a transaction, and panics on error.  Returns an *sqlx.Tx instead
// of an *sql.Tx.
func (db *DB) MustBegin() *Tx {
	tx, err := db.Beginx()
	if err != nil {
		panic(err)
	}
	return tx
}

// Beginx begins a transaction and returns an *sqlx.Tx instead of an *sql.Tx.
func (db *DB) Beginx() (*Tx, error) {
	tx, err := db.DB.Begin()
	if err != nil {
		return nil, err
	}
	return &Tx{Tx: tx, driverName: db.driverName, unsafe: db.unsafe, Mapper: db.Mapper}, err
}

// Begin starts a transaction and do the given handle. The default isolation level
// is dependent on the driver.
//
// With uses context.Background internally; to specify the context, use
// With Tx.
func (db *DB) With(handle func(tx *sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = handle(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// BeginTx starts a transaction and do the given handle.
//
// The provided context is used until the transaction is committed or rolled back.
// If the context is canceled, the sql package will roll back
// the transaction. Tx.Commit will return an error if the context provided to
// BeginTx is canceled.
//
// The provided TxOptions is optional and may be nil if defaults should be used.
// If a non-default isolation level is used that the driver doesn't support,
// an error will be returned.
func (db *DB) WithTx(ctx context.Context, opts *sql.TxOptions, handle func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = handle(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Withx begins a transaction and returns an *sqlx.Tx instead of an *sql.Tx and do the given handle.
func (db *DB) Withx(handle func(tx *Tx) error) error {
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = handle(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// WithTxx begins a transaction and returns an *sqlx.Tx instead of an
// *sql.Tx and do the give handle.
//
// The provided context is used until the transaction is committed or rolled
// back. If the context is canceled, the sql package will roll back the
// transaction. Tx.Commit will return an error if the context provided to
// BeginxContext is canceled.
func (db *DB) WithTxx(ctx context.Context, opts *sql.TxOptions, handle func(tx *Tx) error) error {
	tx, err := db.BeginTxx(ctx, opts)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = handle(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// In expands slice values in args, returning the modified query string
// and a new arg list that can be executed by a database. The `query` should
// use the `?` bindVar.  The return value uses had rebinded bindvar type.
func (db *DB) In(query string, args ...any) (string, []any, error) {
	q, params, err := In(query, args...)
	if err != nil {
		return "", nil, err
	}
	return db.Rebind(q), params, nil
}

// InExec executes a query without returning any rows for in.
// The args are for any placeholder parameters in the query.
//
// InExec uses context.Background internally; to specify the context, use
// ExecContext.
func (db *DB) InExec(query string, args ...any) (sql.Result, error) {
	return InExec(db, query, args...)
}

// InSelect using this DB but for in.
// Any placeholder parameters are replaced with supplied args.
func (db *DB) InSelect(dest any, query string, args ...any) error {
	return InSelect(db, dest, query, args...)
}

// InGet using this DB but for in.
// Any placeholder parameters are replaced with supplied args.
// An error is returned if the result set is empty.
func (db *DB) InGet(dest any, query string, args ...any) error {
	return InGet(db, dest, query, args...)
}

// Queryx queries the database and returns an *sqlx.Rows.
// Any placeholder parameters are replaced with supplied args.
func (db *DB) Queryx(query string, args ...any) (*Rows, error) {
	r, err := db.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	return &Rows{Rows: r, unsafe: db.unsafe, Mapper: db.Mapper}, err
}

// QueryRowx queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func (db *DB) QueryRowx(query string, args ...any) *Row {
	rows, err := db.DB.Query(query, args...)
	return &Row{rows: rows, err: err, unsafe: db.unsafe, Mapper: db.Mapper}
}

// MustExec (panic) runs MustExec using this database.
// Any placeholder parameters are replaced with supplied args.
func (db *DB) MustExec(query string, args ...any) sql.Result {
	return MustExec(db, query, args...)
}

// MustExec (panic) runs MustExec using this database for in.
// Any placeholder parameters are replaced with supplied args.
func (db *DB) MustInExec(query string, args ...any) sql.Result {
	return MustInExec(db, query, args...)
}

// Preparex returns an sqlx.Stmt instead of a sql.Stmt
func (db *DB) Preparex(query string) (*Stmt, error) {
	return Preparex(db, query)
}

// PrepareNamed returns an sqlx.NamedStmt
func (db *DB) PrepareNamed(query string) (*NamedStmt, error) {
	return prepareNamed(db, query)
}

// Conn is a wrapper around sql.Conn with extra functionality
type Conn struct {
	*sql.Conn
	driverName string
	unsafe     bool
	Mapper     *reflectx.Mapper
}

// Tx is an sqlx wrapper around sql.Tx with extra functionality
type Tx struct {
	*sql.Tx
	driverName string
	unsafe     bool
	Mapper     *reflectx.Mapper
}

// DriverName returns the driverName used by the DB which began this transaction.
func (tx *Tx) DriverName() string {
	return tx.driverName
}

// Rebind a query within a transaction's bindvar type.
func (tx *Tx) Rebind(query string) string {
	return Rebind(BindType(tx.driverName), query)
}

// Unsafe returns a version of Tx which will silently succeed to scan when
// columns in the SQL result have no fields in the destination struct.
func (tx *Tx) Unsafe() *Tx {
	return &Tx{Tx: tx.Tx, driverName: tx.driverName, unsafe: true, Mapper: tx.Mapper}
}

// BindNamed binds a query within a transaction's bindvar type.
func (tx *Tx) BindNamed(query string, arg any) (string, []any, error) {
	return bindNamedMapper(BindType(tx.driverName), query, arg, tx.Mapper)
}

// NamedQuery within a transaction.
// Any named placeholder parameters are replaced with fields from arg.
func (tx *Tx) NamedQuery(query string, arg any) (*Rows, error) {
	return NamedQuery(tx, query, arg)
}

// NamedExec a named query within a transaction.
// Any named placeholder parameters are replaced with fields from arg.
func (tx *Tx) NamedExec(query string, arg any) (sql.Result, error) {
	return NamedExec(tx, query, arg)
}

// In expands slice values in args, returning the modified query string
// and a new arg list that can be executed by a database. The `query` should
// use the `?` bindVar.  The return value uses had rebinded bindvar type.
func (tx *Tx) In(query string, args ...any) (string, []any, error) {
	q, params, err := In(query, args...)
	if err != nil {
		return "", nil, err
	}
	return tx.Rebind(q), params, nil
}

// InExec executes a query that doesn't return rows for in.
// For example: an INSERT and UPDATE.
//
// Exec uses context.Background internally; to specify the context, use
// ExecContext.
func (tx *Tx) InExec(query string, args ...any) (sql.Result, error) {
	return InExec(tx, query, args...)
}

// InSelect within a transaction for in.
// Any placeholder parameters are replaced with supplied args.
func (tx *Tx) InSelect(dest any, query string, args ...any) error {
	return InSelect(tx, dest, query, args...)
}

// Get within a transaction for in.
// Any placeholder parameters are replaced with supplied args.
// An error is returned if the result set is empty.
func (tx *Tx) InGet(dest any, query string, args ...any) error {
	return InGet(tx, dest, query, args...)
}

// Select within a transaction.
// Any placeholder parameters are replaced with supplied args.
func (tx *Tx) Select(dest any, query string, args ...any) error {
	return Select(tx, dest, query, args...)
}

// Queryx within a transaction.
// Any placeholder parameters are replaced with supplied args.
func (tx *Tx) Queryx(query string, args ...any) (*Rows, error) {
	r, err := tx.Tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	return &Rows{Rows: r, unsafe: tx.unsafe, Mapper: tx.Mapper}, err
}

// QueryRowx within a transaction.
// Any placeholder parameters are replaced with supplied args.
func (tx *Tx) QueryRowx(query string, args ...any) *Row {
	rows, err := tx.Tx.Query(query, args...)
	return &Row{rows: rows, err: err, unsafe: tx.unsafe, Mapper: tx.Mapper}
}

// Get within a transaction.
// Any placeholder parameters are replaced with supplied args.
// An error is returned if the result set is empty.
func (tx *Tx) Get(dest any, query string, args ...any) error {
	return Get(tx, dest, query, args...)
}

// MustExec runs MustExec within a transaction.
// Any placeholder parameters are replaced with supplied args.
func (tx *Tx) MustExec(query string, args ...any) sql.Result {
	return MustExec(tx, query, args...)
}

// MustInExec runs MustExec within a transaction for in.
// Any placeholder parameters are replaced with supplied args.
func (tx *Tx) MustInExec(query string, args ...any) sql.Result {
	return MustInExec(tx, query, args...)
}

// Preparex  a statement within a transaction.
func (tx *Tx) Preparex(query string) (*Stmt, error) {
	return Preparex(tx, query)
}

// Stmtx returns a version of the prepared statement which runs within a transaction.  Provided
// stmt can be either *sql.Stmt or *sqlx.Stmt.
func (tx *Tx) Stmtx(stmt any) *Stmt {
	var s *sql.Stmt
	switch v := stmt.(type) {
	case Stmt:
		s = v.Stmt
	case *Stmt:
		s = v.Stmt
	case *sql.Stmt:
		s = v
	default:
		panic(fmt.Sprintf("non-statement type %v passed to Stmtx", reflect.ValueOf(stmt).Type()))
	}
	return &Stmt{Stmt: tx.Stmt(s), Mapper: tx.Mapper}
}

// NamedStmt returns a version of the prepared statement which runs within a transaction.
func (tx *Tx) NamedStmt(stmt *NamedStmt) *NamedStmt {
	return &NamedStmt{
		QueryString: stmt.QueryString,
		Params:      stmt.Params,
		Stmt:        tx.Stmtx(stmt.Stmt),
	}
}

// PrepareNamed returns an sqlx.NamedStmt
func (tx *Tx) PrepareNamed(query string) (*NamedStmt, error) {
	return prepareNamed(tx, query)
}

// Stmt is an sqlx wrapper around sql.Stmt with extra functionality
type Stmt struct {
	*sql.Stmt
	unsafe bool
	Mapper *reflectx.Mapper
}

// Unsafe returns a version of Stmt which will silently succeed to scan when
// columns in the SQL result have no fields in the destination struct.
func (s *Stmt) Unsafe() *Stmt {
	return &Stmt{Stmt: s.Stmt, unsafe: true, Mapper: s.Mapper}
}

// Select using the prepared statement.
// Any placeholder parameters are replaced with supplied args.
func (s *Stmt) Select(dest any, args ...any) error {
	return Select(&qStmt{s}, dest, "", args...)
}

// Get using the prepared statement.
// Any placeholder parameters are replaced with supplied args.
// An error is returned if the result set is empty.
func (s *Stmt) Get(dest any, args ...any) error {
	return Get(&qStmt{s}, dest, "", args...)
}

// MustExec (panic) using this statement.  Note that the query portion of the error
// output will be blank, as Stmt does not expose its query.
// Any placeholder parameters are replaced with supplied args.
func (s *Stmt) MustExec(args ...any) sql.Result {
	return MustExec(&qStmt{s}, "", args...)
}

// QueryRowx using this statement.
// Any placeholder parameters are replaced with supplied args.
func (s *Stmt) QueryRowx(args ...any) *Row {
	qs := &qStmt{s}
	return qs.QueryRowx("", args...)
}

// Queryx using this statement.
// Any placeholder parameters are replaced with supplied args.
func (s *Stmt) Queryx(args ...any) (*Rows, error) {
	qs := &qStmt{s}
	return qs.Queryx("", args...)
}

// qStmt is an unexposed wrapper which lets you use a Stmt as a Queryer & Execer by
// implementing those interfaces and ignoring the `query` argument.
type qStmt struct{ *Stmt }

func (q *qStmt) Query(query string, args ...any) (*sql.Rows, error) {
	return q.Stmt.Query(args...)
}

func (q *qStmt) Queryx(query string, args ...any) (*Rows, error) {
	r, err := q.Stmt.Query(args...)
	if err != nil {
		return nil, err
	}
	return &Rows{Rows: r, unsafe: q.Stmt.unsafe, Mapper: q.Stmt.Mapper}, err
}

func (q *qStmt) QueryRowx(query string, args ...any) *Row {
	rows, err := q.Stmt.Query(args...)
	return &Row{rows: rows, err: err, unsafe: q.Stmt.unsafe, Mapper: q.Stmt.Mapper}
}

func (q *qStmt) Exec(query string, args ...any) (sql.Result, error) {
	return q.Stmt.Exec(args...)
}

// Rows is a wrapper around sql.Rows which caches costly reflect operations
// during a looped StructScan
type Rows struct {
	*sql.Rows
	unsafe bool
	Mapper *reflectx.Mapper
	// these fields cache memory use for a rows during iteration w/ structScan
	started bool
	fields  [][]int
	values  []any
}

// SliceScan using this Rows.
func (r *Rows) SliceScan() ([]any, error) {
	return SliceScan(r)
}

// MapScan using this Rows.
func (r *Rows) MapScan(dest map[string]any) error {
	return MapScan(r, dest)
}

// StructScan is like sql.Rows.Scan, but scans a single Row into a single Struct.
// Use this and iterate over Rows manually when the memory load of Select() might be
// prohibitive.  *Rows.StructScan caches the reflect work of matching up column
// positions to fields to avoid that overhead per scan, which means it is not safe
// to run StructScan on the same Rows instance with different struct types.
func (r *Rows) StructScan(dest any) error {
	v := reflect.ValueOf(dest)

	if v.Kind() != reflect.Ptr {
		return errors.New("must pass a pointer, not a value, to StructScan destination")
	}

	v = v.Elem()

	if !r.started {
		columns, err := r.Columns()
		if err != nil {
			return err
		}
		m := r.Mapper

		r.fields = m.TraversalsByName(v.Type(), columns)
		// if we are not unsafe and are missing fields, return an error
		if f, err := missingFields(r.fields); err != nil && !r.unsafe {
			return fmt.Errorf("missing destination name %s in %T", columns[f], dest)
		}
		r.values = make([]any, len(columns))
		r.started = true
	}

	err := fieldsByTraversal(v, r.fields, r.values, true)
	if err != nil {
		return err
	}
	// scan into the struct field pointers and append to our results
	err = r.Scan(r.values...)
	if err != nil {
		return err
	}
	return r.Err()
}

// Connect to a database and verify with a ping.
func Connect(driverName, dataSourceName string) (*DB, error) {
	db, err := Open(driverName, dataSourceName)
	if err != nil {
		return nil, err
	}
	err = db.Ping()
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// MustConnect connects to a database and panics on error.
func MustConnect(driverName, dataSourceName string) *DB {
	db, err := Connect(driverName, dataSourceName)
	if err != nil {
		panic(err)
	}
	return db
}

// Preparex prepares a statement.
func Preparex(p Preparer, query string) (*Stmt, error) {
	s, err := p.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &Stmt{Stmt: s, unsafe: isUnsafe(p), Mapper: mapperFor(p)}, err
}

// Select executes a query using the provided Queryer, and StructScans each row
// into dest, which must be a slice.  If the slice elements are scannable, then
// the result set must have only one column.  Otherwise, StructScan is used.
// The *sql.Rows are closed automatically.
// Any placeholder parameters are replaced with supplied args.
func Select(q Queryer, dest any, query string, args ...any) error {
	rows, err := q.Queryx(query, args...)
	if err != nil {
		return err
	}
	// if something happens here, we want to make sure the rows are Closed
	defer rows.Close()
	return scanAll(rows, dest, false)
}

// Get does a QueryRow using the provided Queryer, and scans the resulting row
// to dest.  If dest is scannable, the result must only have one column.  Otherwise,
// StructScan is used.  Get will return sql.ErrNoRows like row.Scan would.
// Any placeholder parameters are replaced with supplied args.
// An error is returned if the result set is empty.
func Get(q Queryer, dest any, query string, args ...any) error {
	r := q.QueryRowx(query, args...)
	return r.scanAny(dest, false)
}

// InSelect for in scene executes a query using the provided Queryer, and StructScans each row
// into dest, which must be a slice.  If the slice elements are scannable, then
// the result set must have only one column.  Otherwise, StructScan is used.
// The *sql.Rows are closed automatically.
// Any placeholder parameters are replaced with supplied args.
func InSelect(q QueryIn, dest any, query string, args ...any) error {
	newQuery, params, err := q.In(query, args...)
	if err != nil {
		return err
	}
	rows, err := q.Queryx(newQuery, params...)
	if err != nil {
		return err
	}
	// if something happens here, we want to make sure the rows are Closed
	defer rows.Close()
	return scanAll(rows, dest, false)
}

// InGet for in scene does a QueryRow using the provided Queryer, and scans the resulting row
// to dest.  If dest is scannable, the result must only have one column.  Otherwise,
// StructScan is used.  Get will return sql.ErrNoRows like row.Scan would.
// Any placeholder parameters are replaced with supplied args.
// An error is returned if the result set is empty.
func InGet(q QueryIn, dest any, query string, args ...any) error {
	newQuery, params, err := q.In(query, args...)
	if err != nil {
		return err
	}
	r := q.QueryRowx(newQuery, params...)
	return r.scanAny(dest, false)
}

// LoadFile exec's every statement in a file (as a single call to Exec).
// LoadFile may return a nil *sql.Result if errors are encountered locating or
// reading the file at path.  LoadFile reads the entire file into memory, so it
// is not suitable for loading large data dumps, but can be useful for initializing
// schemas or loading indexes.
//
// FIXME: this does not really work with multi-statement files for mattn/go-sqlite3
// or the go-mysql-driver/mysql drivers;  pq seems to be an exception here.  Detecting
// this by requiring something with DriverName() and then attempting to split the
// queries will be difficult to get right, and its current driver-specific behavior
// is deemed at least not complex in its incorrectness.
func LoadFile(e Execer, path string) (*sql.Result, error) {
	realpath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	contents, err := os.ReadFile(realpath)
	if err != nil {
		return nil, err
	}
	res, err := e.Exec(string(contents))
	return &res, err
}

// MustExec execs the query using e and panics if there was an error.
// Any placeholder parameters are replaced with supplied args.
func MustExec(e Execer, query string, args ...any) sql.Result {
	res, err := e.Exec(query, args...)
	if err != nil {
		panic(err)
	}
	return res
}

// MustInExec for in scene execs the query using e and panics if there was an error.
// Any placeholder parameters are replaced with supplied args.
func MustInExec(e ExecIn, query string, args ...any) sql.Result {
	newQuery, params, err := e.In(query, args...)
	if err != nil {
		panic(err)
	}
	res, err := e.Exec(newQuery, params...)
	if err != nil {
		panic(err)
	}
	return res
}

// Exec for in scene executes a query that doesn't return rows.
// For example: an INSERT and UPDATE.
//
// Exec uses context.Background internally; to specify the context, use
// ExecContext.
func InExec(e ExecIn, query string, args ...any) (sql.Result, error) {
	newQuery, params, err := e.In(query, args...)
	if err != nil {
		return nil, err
	}
	return e.Exec(newQuery, params...)
}

// SliceScan using this Rows.
func (r *Row) SliceScan() ([]any, error) {
	return SliceScan(r)
}

// MapScan using this Rows.
func (r *Row) MapScan(dest map[string]any) error {
	return MapScan(r, dest)
}

func (r *Row) scanAny(dest any, structOnly bool) error {
	if r.err != nil {
		return r.err
	}
	if r.rows == nil {
		r.err = sql.ErrNoRows
		return r.err
	}
	defer r.rows.Close()

	v := reflect.ValueOf(dest)
	if v.Kind() != reflect.Ptr {
		return errors.New("must pass a pointer, not a value, to StructScan destination")
	}
	if v.IsNil() {
		return errors.New("nil pointer passed to StructScan destination")
	}

	base := reflectx.Deref(v.Type())
	scannable := isScannable(base)

	if structOnly && scannable {
		return structOnlyError(base)
	}

	columns, err := r.Columns()
	if err != nil {
		return err
	}

	if scannable && len(columns) > 1 {
		return fmt.Errorf("scannable dest type %s with >1 columns (%d) in result", base.Kind(), len(columns))
	}

	if scannable {
		return r.Scan(dest)
	}

	m := r.Mapper

	fields := m.TraversalsByName(v.Type(), columns)
	// if we are not unsafe and are missing fields, return an error
	if f, err := missingFields(fields); err != nil && !r.unsafe {
		return fmt.Errorf("missing destination name %s in %T", columns[f], dest)
	}
	values := make([]any, len(columns))

	err = fieldsByTraversal(v, fields, values, true)
	if err != nil {
		return err
	}
	// scan into the struct field pointers and append to our results
	return r.Scan(values...)
}

// StructScan a single Row into dest.
func (r *Row) StructScan(dest any) error {
	return r.scanAny(dest, true)
}

// SliceScan a row, returning a []any with values similar to MapScan.
// This function is primarily intended for use where the number of columns
// is not known.  Because you can pass an []any directly to Scan,
// it's recommended that you do that as it will not have to allocate new
// slices per row.
func SliceScan(r ColScanner) ([]any, error) {
	// ignore r.started, since we needn't use reflect for anything.
	columns, err := r.Columns()
	if err != nil {
		return []any{}, err
	}

	values := make([]any, len(columns))
	for i := range values {
		values[i] = new(any)
	}

	err = r.Scan(values...)

	if err != nil {
		return values, err
	}

	for i := range columns {
		values[i] = *(values[i].(*any))
	}

	return values, r.Err()
}

// MapScan scans a single Row into the dest map[string]any.
// Use this to get results for SQL that might not be under your control
// (for instance, if you're building an interface for an SQL server that
// executes SQL from input).  Please do not use this as a primary interface!
// This will modify the map sent to it in place, so reuse the same map with
// care.  Columns which occur more than once in the result will overwrite
// each other!
func MapScan(r ColScanner, dest map[string]any) error {
	// ignore r.started, since we needn't use reflect for anything.
	columns, err := r.Columns()
	if err != nil {
		return err
	}

	values := make([]any, len(columns))
	for i := range values {
		values[i] = new(any)
	}

	err = r.Scan(values...)
	if err != nil {
		return err
	}

	for i, column := range columns {
		dest[column] = *(values[i].(*any))
	}

	return r.Err()
}

type rowsi interface {
	Close() error
	Columns() ([]string, error)
	Err() error
	Next() bool
	Scan(...any) error
}

// structOnlyError returns an error appropriate for type when a non-scannable
// struct is expected but something else is given
func structOnlyError(t reflect.Type) error {
	isStruct := t.Kind() == reflect.Struct
	isScanner := reflect.PtrTo(t).Implements(_scannerInterface)
	if !isStruct {
		return fmt.Errorf("expected %s but got %s", reflect.Struct, t.Kind())
	}
	if isScanner {
		return fmt.Errorf("structscan expects a struct dest but the provided struct type %s implements scanner", t.Name())
	}
	return fmt.Errorf("expected a struct, but struct %s has no exported fields", t.Name())
}

// scanAll scans all rows into a destination, which must be a slice of any
// type.  It resets the slice length to zero before appending each element to
// the slice.  If the destination slice type is a Struct, then StructScan will
// be used on each row.  If the destination is some other kind of base type,
// then each row must only have one column which can scan into that type.  This
// allows you to do something like:
//
//	rows, _ := db.Query("select id from people;")
//	var ids []int
//	scanAll(rows, &ids, false)
//
// and ids will be a list of the id results.  I realize that this is a desirable
// interface to expose to users, but for now it will only be exposed via changes
// to `Get` and `Select`.  The reason that this has been implemented like this is
// this is the only way to not duplicate reflect work in the new API while
// maintaining backwards compatibility.
func scanAll(rows rowsi, dest any, structOnly bool) error {
	var v, vp reflect.Value

	value := reflect.ValueOf(dest)

	// json.Unmarshal returns errors for these
	if value.Kind() != reflect.Ptr {
		return errors.New("must pass a pointer, not a value, to StructScan destination")
	}
	if value.IsNil() {
		return errors.New("nil pointer passed to StructScan destination")
	}
	direct := reflect.Indirect(value)

	slice, err := baseType(value.Type(), reflect.Slice)
	if err != nil {
		return err
	}
	direct.SetLen(0)

	isPtr := slice.Elem().Kind() == reflect.Ptr
	base := reflectx.Deref(slice.Elem())
	scannable := isScannable(base)

	if structOnly && scannable {
		return structOnlyError(base)
	}

	columns, err := rows.Columns()
	if err != nil {
		return err
	}

	// if it's a base type make sure it only has 1 column;  if not return an error
	if scannable && len(columns) > 1 {
		return fmt.Errorf("non-struct dest type %s with >1 columns (%d)", base.Kind(), len(columns))
	}

	if !scannable {
		var values []any
		var m *reflectx.Mapper

		switch rows.(type) {
		case *Rows:
			m = rows.(*Rows).Mapper
		default:
			m = mapper()
		}

		fields := m.TraversalsByName(base, columns)
		// if we are not unsafe and are missing fields, return an error
		if f, err := missingFields(fields); err != nil && !isUnsafe(rows) {
			return fmt.Errorf("missing destination name %s in %T", columns[f], dest)
		}
		values = make([]any, len(columns))

		for rows.Next() {
			// create a new struct type (which returns PtrTo) and indirect it
			vp = reflect.New(base)
			v = reflect.Indirect(vp)

			err = fieldsByTraversal(v, fields, values, true)
			if err != nil {
				return err
			}

			// scan into the struct field pointers and append to our results
			err = rows.Scan(values...)
			if err != nil {
				return err
			}

			if isPtr {
				direct.Set(reflect.Append(direct, vp))
			} else {
				direct.Set(reflect.Append(direct, v))
			}
		}
	} else {
		for rows.Next() {
			vp = reflect.New(base)
			err = rows.Scan(vp.Interface())
			if err != nil {
				return err
			}
			// append
			if isPtr {
				direct.Set(reflect.Append(direct, vp))
			} else {
				direct.Set(reflect.Append(direct, reflect.Indirect(vp)))
			}
		}
	}

	return rows.Err()
}

// FIXME: StructScan was the very first bit of API in sqlx, and now unfortunately
// it doesn't really feel like it's named properly.  There is an incongruency
// between this and the way that StructScan (which might better be ScanStruct
// anyway) works on a rows object.

// StructScan all rows from an sql.Rows or an sqlx.Rows into the dest slice.
// StructScan will scan in the entire rows result, so if you do not want to
// allocate structs for the entire result, use Queryx and see sqlx.Rows.StructScan.
// If rows is sqlx.Rows, it will use its mapper, otherwise it will use the default.
func StructScan(rows rowsi, dest any) error {
	return scanAll(rows, dest, true)

}

// reflect helpers

func baseType(t reflect.Type, expected reflect.Kind) (reflect.Type, error) {
	t = reflectx.Deref(t)
	if t.Kind() != expected {
		return nil, fmt.Errorf("expected %s but got %s", expected, t.Kind())
	}
	return t, nil
}

// fieldsByName fills a values interface with fields from the passed value based
// on the traversals in int.  If ptrs is true, return addresses instead of values.
// We write this instead of using FieldsByName to save allocations and map lookups
// when iterating over many rows.  Empty traversals will get an interface pointer.
// Because of the necessity of requesting ptrs or values, it's considered a bit too
// specialized for inclusion in reflectx itself.
func fieldsByTraversal(v reflect.Value, traversals [][]int, values []any, ptrs bool) error {
	v = reflect.Indirect(v)
	if v.Kind() != reflect.Struct {
		return errors.New("argument not a struct")
	}

	for i, traversal := range traversals {
		if len(traversal) == 0 {
			values[i] = new(any)
			continue
		}
		f := reflectx.FieldByIndexes(v, traversal)
		if ptrs {
			values[i] = f.Addr().Interface()
		} else {
			values[i] = f.Interface()
		}
	}
	return nil
}

func missingFields(transversals [][]int) (field int, err error) {
	for i, t := range transversals {
		if len(t) == 0 {
			return i, errors.New("missing field")
		}
	}
	return 0, nil
}
