package main

import (
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

func main() {
	if err := run(); err != nil {
		panic(err)
	}
}

func run() error {
	db, err := sqlx.Connect("postgres", "postgres://postgres:password@0.0.0.0:5432/finance_invoice?sslmode=disable")
	if err != nil {
		return err
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	tx, err := db.Beginx()
	if err != nil {
		return err
	}

	go func() {
		time.Sleep(10 * time.Second)
		_ = tx.Rollback()
	}()

	rows, err := db.Query("select count(*) from capacitor")
	if err != nil {
		return err
	}

	if err := rows.Close(); err != nil {
		return fmt.Errorf("close first rows: %w", err)
	}

	rows, err = db.Query("select count(*) from capacitor")
	if err != nil {
		return err
	}

	if err := rows.Close(); err != nil {
		return fmt.Errorf("close second rows: %w", err)
	}

	return nil
}
