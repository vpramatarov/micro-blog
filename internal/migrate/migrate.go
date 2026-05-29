package migrate

import (
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
	"github.com/vpramatarov/micro-blog/cmd"
)

const DIR string = "migrate/migrations"

func Up(db *sql.DB) (int64, error) {
	if err := Setup(); err != nil {
		return 0, nil
	}

	if err := goose.Up(db, DIR); err != nil {
		return 0, fmt.Errorf("goose up: %w", err)
	}

	v, err := goose.GetDBVersion(db)
	if err != nil {
		return 0, fmt.Errorf("get db version: %w", err)
	}

	return v, nil
}

func Setup() error {
	goose.SetBaseFS(cmd.EmbedMigrations)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}

	return nil
}
