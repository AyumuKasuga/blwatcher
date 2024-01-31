// We don't need real migrations, we can just `CREATE TABLE IF NOT EXISTS` .
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
)

var createTablesSQL = `
CREATE TABLE IF NOT EXISTS events (
	id SERIAL PRIMARY KEY,
	date TIMESTAMP NOT NULL,
	contract TEXT NOT NULL,
	address TEXT NOT NULL,
	tx_hash TEXT NOT NULL,
	block_number BIGINT NOT NULL,
	event_type TEXT NOT NULL,
	amount BIGINT NOT NULL
);
`

func main() {
	ctx := context.Background()
	connString := os.Getenv("DATABASE_URL")
	if connString == "" {
		connString = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
	}
	conn, err := pgx.Connect(context.Background(), connString)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to connect to database: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf(createTablesSQL)
	defer conn.Close(context.Background())
	tx, err := conn.Begin(ctx)
	if err != nil {
		panic(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(
		ctx,
		createTablesSQL,
	)
	tx.Commit(ctx)
}
