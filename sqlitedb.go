package main

import (
	"context"
	"fmt"
	"strings"

	"database/sql"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"

	sqlite3 "github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/ext/unicode"
)

func init() {
	sqlite3.AutoExtension(func(c *sqlite3.Conn) error {
		return unicode.RegisterCollation(c, "en", "icu")
	})
}

const (
	CcDb = "/var/local/cc.db"
)

type DbMode string

const (
	ROMode DbMode = "ro"
	RWMode DbMode = "rw"
)

type DB struct {
	db *sql.DB
}

func (db *DB) close() error {
	return db.db.Close()
}

func newCcDb(mode DbMode) (*DB, error) {
	db, err := sql.Open("sqlite3", "file:"+CcDb+"?mode="+string(mode))
	if err != nil {
		return &DB{}, err
	}

	return &DB{db: db}, nil
}

func (db *DB) cloneTable(source, target, targetDb string) error {
	// Fetch the schema
	stmt := fmt.Sprintf(`
	SELECT sql FROM main.sqlite_schema
	WHERE type='table' AND tbl_name='%s';
	`, source)

	var sqlStmt string
	if err := db.db.QueryRow(stmt).Scan(&sqlStmt); err != nil {
		return fmt.Errorf("Failed to get SQL schema of table %s: %w", source, err)
	}

	// Recreate it under the proper db/name
	stmt = strings.Replace(
		sqlStmt, "CREATE TABLE "+source, "CREATE TABLE "+targetDb+"."+target, 1,
	)
	if _, err := db.db.Exec(stmt); err != nil {
		return fmt.Errorf("Failed to create target table %s: %w", target, err)
	}

	// Also clone the indexes
	stmt = fmt.Sprintf(`
	SELECT sql FROM main.sqlite_schema
	WHERE type='index' AND tbl_name='%s' AND sql IS NOT NULL;
	`, source)

	rows, err := db.db.Query(stmt)
	if err != nil {
		return fmt.Errorf("Failed to get indexes SQL of table %s: %w", source, err)
	}
	defer rows.Close()

	var indexes []string
	for rows.Next() {
		if err := rows.Scan(&sqlStmt); err != nil {
			return fmt.Errorf("Failed to get indexes SQL of table %s: %w", source, err)
		}

		indexes = append(indexes, sqlStmt)
	}

	for _, index := range indexes {
		// Add IF NOT EXISTS to the CREATE INDEX or CREATE UNIQUE INDEX statements
		// The schema prefix goes on the index name, not the table name, so also replace here
		stmt = strings.Replace(
			index, "CREATE INDEX ",
			"CREATE INDEX IF NOT EXISTS "+targetDb+"."+target+"_", 1,
		)
		stmt = strings.Replace(
			stmt, "CREATE UNIQUE INDEX ",
			"CREATE UNIQUE INDEX IF NOT EXISTS "+targetDb+"."+target+"_", 1,
		)

		// Replace the ON condition for the target table
		stmt = strings.Replace(stmt, "ON "+source, "ON "+target, 1)

		if _, err := db.db.Exec(stmt); err != nil {
			return fmt.Errorf("Failed to clone index of table %s: %w", source, err)
		}
	}

	return nil
}

func (db *DB) exportEntriesColls(partial string, excludeColls []string) error {
	if err := db.cloneTable("Entries", "entries_colls", partial); err != nil {
		return err
	}

	var stmt string
	args := make([]any, len(excludeColls))

	if len(excludeColls) == 0 {
		stmt = fmt.Sprintf(`
		INSERT INTO %s.entries_colls
		SELECT * FROM main.Entries
		WHERE p_type='Collection';
		`, partial)
	} else {
		placeholders := make([]string, len(excludeColls))
		for i, v := range excludeColls {
			placeholders[i] = "?"
			args[i] = strings.ToUpper(v)
		}

		stmt = fmt.Sprintf(`
		INSERT INTO %s.entries_colls
		SELECT * FROM main.Entries
		WHERE p_type='Collection'
			AND UPPER(p_titles_0_nominal) NOT IN (%s);
		`, partial, strings.Join(placeholders, ","))
	}

	_, err := db.db.Exec(stmt, args...)
	if err != nil {
		return fmt.Errorf("Failed to export collections from Entries table: %w", err)
	}

	return nil
}

func (db *DB) exportEntriesBooks(partial string, excludeDocs []string) error {
	if err := db.cloneTable("Entries", "entries_books", partial); err != nil {
		return err
	}

	var stmt string
	args := make([]any, len(excludeDocs))

	if len(excludeDocs) == 0 {
		stmt = fmt.Sprintf(`
		INSERT INTO %s.entries_books
		SELECT * FROM main.Entries
		WHERE p_type='Entry:Item'
			AND p_isVisibleInHome=1
			AND p_isArchived=0;
		`, partial)
	} else {
		placeholders := make([]string, len(excludeDocs))
		for i, v := range excludeDocs {
			placeholders[i] = "?"
			args[i] = v
		}

		stmt = fmt.Sprintf(`
		INSERT INTO %s.entries_books
		SELECT * FROM main.Entries
		WHERE p_type='Entry:Item'
			AND p_isVisibleInHome=1
			AND p_isArchived=0
			AND p_location NOT IN (%s);
		`, partial, strings.Join(placeholders, ","))
	}

	_, err := db.db.Exec(stmt, args...)
	if err != nil {
		return fmt.Errorf("Failed to export books from Entries table: %w", err)
	}

	return nil
}

func (db *DB) exportCollectionMappings(partial string, exportedCollsTable string) error {
	if err := db.cloneTable("Collections", "collections", partial); err != nil {
		return err
	}

	stmt := fmt.Sprintf(`
	INSERT INTO %s.collections
	SELECT * FROM main.Collections
	WHERE i_collection_uuid IN (
		SELECT p_uuid FROM %s.%s
	);
	`, partial, partial, exportedCollsTable)

	_, err := db.db.Exec(stmt)
	if err != nil {
		return fmt.Errorf("Failed to export collection mappings from Collections table: %w", err)
	}

	return nil
}

func (db *DB) exportData(dbPath string, excludeColls []string, excludeDocs []string) error {
	_, err := db.db.Exec(`ATTACH ? AS partial;`, dbPath)
	if err != nil {
		return fmt.Errorf("Failed to attach partial DB: %w", err)
	}

	err = db.exportEntriesColls("partial", excludeColls)
	if err != nil {
		return err
	}

	err = db.exportEntriesBooks("partial", excludeDocs)
	if err != nil {
		return err
	}

	err = db.exportCollectionMappings("partial", "entries_colls")
	if err != nil {
		return err
	}

	return nil
}

func (db *DB) usedThumbnails() ([]string, error) {
	thumbnails := []string{}

	rows, err := db.db.Query(`
	SELECT p_thumbnail FROM partial.entries_colls
	UNION
	SELECT p_thumbnail FROM partial.entries_books;
	`)
	if err != nil {
		return thumbnails, fmt.Errorf("Failed to fetch tables: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var thumbnail sql.NullString
		err = rows.Scan(&thumbnail)
		if err != nil {
			return thumbnails, fmt.Errorf("Failed to get row: %w", err)
		}

		if thumbnail.Valid {
			thumbnails = append(thumbnails, thumbnail.String)
		}
	}

	return thumbnails, nil
}

func (db *DB) tableColumns(tx *sql.Tx, schema, table string) ([]string, error) {
	stmt := fmt.Sprintf(`SELECT name FROM %s.pragma_table_info('%s');`, schema, table)
	rows, err := tx.Query(stmt)
	if err != nil {
		return []string{}, fmt.Errorf(
			"Failed to get column names from %s.%s: %w", schema, table, err,
		)
	}

	var cols []string

	var col string
	for rows.Next() {
		if err := rows.Scan(&col); err != nil {
			return []string{}, fmt.Errorf(
				"Failed to get column names from %s.%s: %w", schema, table, err,
			)
		}

		cols = append(cols, col)
	}

	return cols, nil
}

func (db *DB) importEntriesColls(tx *sql.Tx, partial string) error {
	// First insert missing collections
	insertStmt := fmt.Sprintf(`
	INSERT INTO main.Entries
	SELECT p.* FROM %s.entries_colls p
	LEFT JOIN main.Entries m
		ON m.p_titles_0_nominal = p.p_titles_0_nominal
	WHERE m.p_titles_0_nominal IS NULL;
	`, partial)
	_, err := tx.Exec(insertStmt)
	if err != nil {
		return fmt.Errorf("Failed to import new collections from partial entries table: %w", err)
	}

	cols, err := db.tableColumns(tx, "main", "Entries")
	if err != nil {
		return err
	}

	var setClauses []string
	for _, c := range cols {
		// Skip since this is the condition column
		if c == "p_titles_0_nominal" {
			continue
		}

		clause := fmt.Sprintf(`%s = p.%s`, c, c)
		setClauses = append(setClauses, clause)
	}
	setSQL := strings.Join(setClauses, ", ")

	updateStmt := fmt.Sprintf(`
	UPDATE main.Entries
	SET %s
	FROM %s.entries_colls p
	WHERE p.p_titles_0_nominal = main.Entries.p_titles_0_nominal;
	`, setSQL, partial)

	if _, err := tx.Exec(updateStmt); err != nil {
		return fmt.Errorf("Failed to update collections from partial entries table: %w", err)
	}

	return nil
}

func (db *DB) importEntriesBooks(tx *sql.Tx, partial string) error {
	cols, err := db.tableColumns(tx, "main", "Entries")
	if err != nil {
		return err
	}

	// Will work due to the unique constraint on the p_location column
	stmt := fmt.Sprintf(`
	INSERT OR REPLACE INTO main.Entries (%s)
	SELECT %s FROM %s.entries_books;
	`, strings.Join(cols, ", "), strings.Join(cols, ", "), partial)

	if _, err := tx.Exec(stmt); err != nil {
		return fmt.Errorf("Failed to import books from partial entries table: %w", err)
	}

	return nil
}

func (db *DB) importCollectionMappings(tx *sql.Tx, partial string) error {
	cols, err := db.tableColumns(tx, "main", "Collections")
	if err != nil {
		return err
	}

	// Also will work due to the unique constraint on i_collection_uuid and i_member_uuid
	stmt := fmt.Sprintf(`
	INSERT OR REPLACE INTO main.Collections (%s)
	SELECT %s FROM %s.collections
	`, strings.Join(cols, ", "), strings.Join(cols, ", "), partial)

	if _, err := tx.Exec(stmt); err != nil {
		return fmt.Errorf("Failed to import collection mappings table: %w", err)
	}

	// And also clean up old collections that have been updated and their ID has changed
	stmt = fmt.Sprintf(`
	DELETE FROM main.Collections
	WHERE NOT EXISTS (
		SELECT 1 FROM main.Entries e
		WHERE p_type='Collection'
			AND e.p_uuid = main.Collections.i_collection_uuid
	);
	`)

	if _, err := tx.Exec(stmt); err != nil {
		return fmt.Errorf("Failed to clean up old collections: %w", err)
	}

	return nil
}

func (db *DB) reindex(tx *sql.Tx) error {
	_, err := tx.Exec(`REINDEX;`)
	return err
}

func (db *DB) importData(dbPath string) error {
	_, err := db.db.Exec(`ATTACH ? AS partial;`, dbPath)
	if err != nil {
		return fmt.Errorf("Failed to attach partial DB: %w", err)
	}

	tx, err := db.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// If the index are not up to date, it will fail to import data
	if err := db.reindex(tx); err != nil {
		return fmt.Errorf("Failed to reindex DB: %w", err)
	}

	if err = db.importEntriesColls(tx, "partial"); err != nil {
		return err
	}

	if err = db.importEntriesBooks(tx, "partial"); err != nil {
		return err
	}

	if err = db.importCollectionMappings(tx, "partial"); err != nil {
		return err
	}

	// Another just to be safe
	if err := db.reindex(tx); err != nil {
		return fmt.Errorf("Failed to reindex DB: %w", err)
	}

	return tx.Commit()
}
