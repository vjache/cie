package sample

// Backend wraps a database connection.
type Backend struct {
	db *DB
}

// DB is a concrete database type.
type DB struct{}

// Query executes a query on the database.
func (d *DB) Query(sql string) string {
	return sql
}

// Query on Backend delegates to its db field.
// The method name "Query" matches the callee "db.Query()" — this tests
// that the parser correctly produces an unresolved call for db.Query()
// instead of silently dropping it as a self-call.
func (b *Backend) Query(sql string) string {
	return b.db.Query(sql)
}
