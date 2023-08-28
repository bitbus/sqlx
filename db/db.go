package db

import (
	"context"

	"github.com/bitbus/sqlx"
)

// StructScan[T] all rows from an sql.Rows or an sqlx.Rows into the dest slice.
// StructScan[T] will scan in the entire rows result, so if you do not want to
// allocate structs for the entire result, use Queryx and see sqlx.Rows.StructScan.
// If rows is sqlx.Rows, it will use its mapper, otherwise it will use the default.
func StructScan[T any](rows *sqlx.Rows) (dest *T, err error) {
	dest = new(T)
	err = sqlx.StructScan(rows, dest)
	return
}

// Get[T] using the prepared statement.
// Any placeholder parameters are replaced with supplied args.
// An error is returned if the result set is empty.
func Get[T any](stmt *sqlx.Stmt, args ...any) (dest *T, err error) {
	dest = new(T)
	err = stmt.Get(dest, args...)
	return
}

// GetContext[T] using the prepared statement.
// Any placeholder parameters are replaced with supplied args.
// An error is returned if the result set is empty.
func GetContext[T any](ctx context.Context, stmt *sqlx.Stmt, args ...any) (dest *T, err error) {
	dest = new(T)
	err = stmt.GetContext(ctx, dest, args...)
	return
}

// Select[T] using the prepared statement.
// Any placeholder parameters are replaced with supplied args.
func Select[T any](stmt *sqlx.Stmt, args ...any) (dest *T, err error) {
	dest = new(T)
	err = stmt.Select(dest, args...)
	return
}

// SelectContext[T] using the prepared statement.
// Any placeholder parameters are replaced with supplied args.
func SelectContext[T any](ctx context.Context, stmt *sqlx.Stmt, args ...any) (dest *T, err error) {
	dest = new(T)
	err = stmt.SelectContext(ctx, dest, args...)
	return
}

// NamedGet[T] using this NamedStmt
// Any named placeholder parameters are replaced with fields from arg.
func NamedGet[T any](stmt *sqlx.NamedStmt, arg any) (dest *T, err error) {
	dest = new(T)
	err = stmt.Get(dest, arg)
	return
}

// NamedGetContext using this NamedStmt
// Any named placeholder parameters are replaced with fields from arg.
func NamedGetContext[T any](ctx context.Context, stmt *sqlx.NamedStmt, arg any) (dest *T, err error) {
	dest = new(T)
	err = stmt.GetContext(ctx, dest, arg)
	return
}

// NamedSelect using this NamedStmt
// Any named placeholder parameters are replaced with fields from arg.
func NamedSelect[T any](stmt *sqlx.NamedStmt, arg any) (dest *T, err error) {
	dest = new(T)
	err = stmt.Select(dest, arg)
	return
}

// NamedSelectContext using this NamedStmt
// Any named placeholder parameters are replaced with fields from arg.
func NamedSelectContext[T any](ctx context.Context, stmt *sqlx.NamedStmt, arg any) (dest *T, err error) {
	dest = new(T)
	err = stmt.SelectContext(ctx, dest, arg)
	return
}

// InGet[T] for in scene does a QueryRow using the provided Queryer, and scans the resulting row
// to dest.  If dest is scannable, the result must only have one column.  Otherwise,
// StructScan is used.  Get will return sql.ErrNoRows like row.Scan would.
// Any placeholder parameters are replaced with supplied args.
// An error is returned if the result set is empty.
func InGet[T any](q sqlx.Queryable, query string, args ...any) (dest *T, err error) {
	dest = new(T)
	err = q.InGet(*dest, query, args...)
	return
}

// InSelect[T] for in scene executes a query using the provided Queryer, and StructScans each row
// into dest, which must be a slice.  If the slice elements are scannable, then
// the result set must have only one column.  Otherwise, StructScan is used.
// The *sql.Rows are closed automatically.
// Any placeholder parameters are replaced with supplied args.
func InSelect[T any](q sqlx.Queryable, query string, args ...any) (dest *T, err error) {
	dest = new(T)
	err = q.InSelect(dest, query, args...)
	return
}
