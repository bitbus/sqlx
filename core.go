package sqlx

import (
	"context"
	"database/sql"
)

var (
	_ Queryable = (*DB)(nil)
	_ Queryable = (*Tx)(nil)
)

// Queryable includes all methods shared by sqlx.DB and sqlx.Tx, allowing
// either type to be used interchangeably.
type Queryable interface {
	Ext
	ExecIn
	QueryIn
	ExecerContext
	PreparerContext
	QueryerContext
	Preparer

	GetContext(context.Context, interface{}, string, ...interface{}) error
	SelectContext(context.Context, interface{}, string, ...interface{}) error
	Get(interface{}, string, ...interface{}) error
	MustExecContext(context.Context, string, ...interface{}) sql.Result
	PreparexContext(context.Context, string) (*Stmt, error)
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
	Select(interface{}, string, ...interface{}) error
	QueryRow(string, ...interface{}) *sql.Row
	PrepareNamedContext(context.Context, string) (*NamedStmt, error)
	PrepareNamed(string) (*NamedStmt, error)
	Preparex(string) (*Stmt, error)
	NamedExec(string, interface{}) (sql.Result, error)
	NamedExecContext(context.Context, string, interface{}) (sql.Result, error)
	MustExec(string, ...interface{}) sql.Result
	NamedQuery(string, interface{}) (*Rows, error)
	InGet(any, string, ...any) error
	InSelect(any, string, ...any) error
	InExec(string, ...any) (sql.Result, error)
	MustInExec(string, ...any) sql.Result
}
