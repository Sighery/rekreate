package main

import (
	"archive/tar"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"database/sql"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"

	sqlite3 "github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/ext/unicode"
)

func init() {
	sqlite3.AutoExtension(func(c *sqlite3.Conn) error {
		return unicode.RegisterCollation(c, "en", "icu")
		// return unicode.RegisterCollationsNeeded(c)
	})
}

const (
	CcDb            = "/var/local/cc.db"
	TimestampFormat = "20060102-150405"
	DocumentsPath   = "/mnt/us/documents/"
	ThumbnailsPath  = "/mnt/us/system/thumbnails/"
)

var DefaultExclusions = [...]string{
	"/mnt/us/documents/My Clippings*",
	"/mnt/us/documents/dictionaries/",
	"/mnt/us/documents/Downloads/",
	"/mnt/us/documents/*.kol",
	"/mnt/us/documents/*.kual",
}

var ExcludedExtensions = map[string]bool{
	".bad_file": true,
}

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

func (db *DB) exportEntriesColls(partial string, excludeColls []string) error {
	var stmt string
	args := make([]any, len(excludeColls))

	if len(excludeColls) == 0 {
		stmt = fmt.Sprintf(`
		CREATE TABLE %s.entries_colls AS
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
		CREATE TABLE %s.entries_colls AS
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
	var stmt string
	args := make([]any, len(excludeDocs))

	if len(excludeDocs) == 0 {
		stmt = fmt.Sprintf(`
		CREATE TABLE %s.entries_books AS
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
		CREATE TABLE %s.entries_books AS
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
	stmt := fmt.Sprintf(`
	CREATE TABLE %s.collections AS
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

type globExclusion []string

func (e *globExclusion) String() string {
	return fmt.Sprint(*e)
}

func (e *globExclusion) Set(value string) error {
	matches, err := filepath.Glob(value)
	if err != nil {
		return fmt.Errorf("Couldn't parse glob: %w", err)
	}

	for _, match := range matches {
		full, err := filepath.Abs(match)
		if err != nil {
			return fmt.Errorf("Couldn't get full path of file: %w", err)
		}

		fileInfo, err := os.Stat(full)
		if err != nil {
			return fmt.Errorf("Couldn't get info of path: %w", err)
		}

		// If this was a directory, find and exclude all the children
		if fileInfo.IsDir() {
			err = e.Set(filepath.Join(full, "**"))
			if err != nil {
				return err
			}
		}

		*e = append(*e, full)
	}

	return nil
}

type exclusion []string

func (e *exclusion) String() string {
	return fmt.Sprint(*e)
}

func (e *exclusion) Set(value string) error {
	*e = append(*e, value)
	return nil
}

// func isBookFile(path string) bool {
// 	if strings.HasSuffix(path, "azw3") {
// 		return true
// 	}
// 	return false
// }

// func filePaths(directory string, exclusions map[string]bool) (ret []string) {
// 	filepath.WalkDir(directory, func(path string, d fs.DirEntry, err error) error {
// 		if err != nil {
// 			return err
// 		}

// 		if _, found := exclusions[path]; found {
// 			// fmt.Printf("Found excluded %s\n", path)
// 			if d.IsDir() {
// 				return fs.SkipDir
// 			}
// 			return nil
// 		}

// 		if d.IsDir() == true && strings.HasSuffix(path, ".sdr") {
// 			return nil
// 		}

// 		// if isBookFile(path) == false {
// 		// 	return nil
// 		// }

// 		fmt.Printf("Visited: %s, isDir: %t\n", path, d.IsDir())
// 		return nil
// 	})
// 	return ret
// }

func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destinationFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destinationFile.Close()

	_, err = io.Copy(destinationFile, sourceFile)
	if err != nil {
		return err
	}

	err = destinationFile.Sync()
	if err != nil {
		return err
	}

	return nil
}

func copyDir(src, dst string, exclusions map[string]bool) error {
	dstDir := filepath.Join(dst, filepath.Base(src))
	err := os.Mkdir(dstDir, 0750)
	if err != nil {
		return err
	}

	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if path == src {
			return nil
		}

		if _, found := exclusions[path]; found {
			if info.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		// Relative path to the src directory
		rel, _ := strings.CutPrefix(path, src)
		destPath := filepath.Join(dstDir, rel)

		if info.IsDir() {
			err = os.Mkdir(destPath, 0750)
			if err != nil {
				return err
			}
		} else {
			err = copyFile(path, destPath)
			if err != nil {
				return err
			}
		}

		return nil
	})
}

func copyThumbnails(thumbnails []string, tempDir string) error {
	dstDir := filepath.Join(tempDir, "thumbnails")
	err := os.Mkdir(dstDir, 0750)
	if err != nil {
		return err
	}

	for _, thumbnail := range thumbnails {
		// Check if file exists in the first place
		if _, err := os.Stat(thumbnail); err != nil {
			log.Printf("WARN - Couldn't find thumbnail %s\n", thumbnail)
			continue
		}

		dstPath := filepath.Join(dstDir, filepath.Base(thumbnail))
		err = copyFile(thumbnail, dstPath)
		if err != nil {
			return err
		}
	}

	return nil
}

type TarExport struct {
	gzWriter  *gzip.Writer
	tarWriter *tar.Writer
}

func NewTarExport(outFile *os.File) (*TarExport, error) {
	gz, err := gzip.NewWriterLevel(outFile, gzip.NoCompression)
	if err != nil {
		return &TarExport{}, err
	}

	tw := tar.NewWriter(gz)

	return &TarExport{
		gzWriter:  gz,
		tarWriter: tw,
	}, nil
}

func (t *TarExport) Close() error {
	err := t.tarWriter.Close()
	if err != nil {
		return err
	}

	err = t.gzWriter.Close()
	if err != nil {
		return err
	}

	return nil
}

func (t *TarExport) CreateDir(relPath string, info os.FileInfo) error {
	if info.IsDir() == false {
		return fmt.Errorf("Passed path is not a directory")
	}

	name := relPath
	if strings.HasSuffix(name, "/") == false {
		name += "/"
	}

	hdr := &tar.Header{
		Name:     name,
		Mode:     int64(info.Mode()),
		ModTime:  info.ModTime(),
		Typeflag: tar.TypeDir,
	}
	return t.tarWriter.WriteHeader(hdr)
}

func (t *TarExport) AddFile(src, relDst string, info os.FileInfo) error {
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = relDst

	if err := t.tarWriter.WriteHeader(hdr); err != nil {
		return err
	}

	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(t.tarWriter, f)
	return err
}

// func tarDirectoryContents(root string, out *os.File) error {
// 	gz := gzip.NewWriter(out)
// 	defer gz.Close()

// 	tw := tar.NewWriter(gz)
// 	defer tw.Close()

// 	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
// 		if err != nil {
// 			return err
// 		}

// 		if path == root {
// 			return nil
// 		}

// 		rel, err := filepath.Rel(root, path)
// 		if err != nil {
// 			return err
// 		}

// 		if info.IsDir() {
// 			hdr := &tar.Header{
// 				Name:     rel + "/",
// 				Mode:     int64(info.Mode()),
// 				ModTime:  info.ModTime(),
// 				Typeflag: tar.TypeDir,
// 			}
// 			return tw.WriteHeader(hdr)
// 		}

// 		hdr, err := tar.FileInfoHeader(info, "")
// 		if err != nil {
// 			return err
// 		}
// 		hdr.Name = rel

// 		if err := tw.WriteHeader(hdr); err != nil {
// 			return err
// 		}

// 		f, err := os.Open(path)
// 		if err != nil {
// 			return err
// 		}
// 		defer f.Close()

// 		_, err = io.Copy(tw, f)
// 		return err
// 	})
// }

func addThumbnailsToTar(thumbnails []string, te *TarExport) error {
	tarDir := "thumbnails"

	dirInfo, err := os.Stat(ThumbnailsPath)
	if err != nil {
		return fmt.Errorf("Failed to stat host thumbnails directory: %w", err)
	}

	err = te.CreateDir(tarDir, dirInfo)
	if err != nil {
		return fmt.Errorf("Failed to create thumbnails dir in tar: %w", err)
	}

	for _, thumbnail := range thumbnails {
		fileInfo, err := os.Stat(thumbnail)
		if err != nil {
			log.Printf("WARN - Couldn't find thumbnail %s\n", thumbnail)
			continue
		}

		tarPath := filepath.Join(tarDir, filepath.Base(thumbnail))
		err = te.AddFile(thumbnail, tarPath, fileInfo)
		if err != nil {
			return err
		}
	}

	return nil
}

func addDocumentsToTar(exclusions globExclusion, te *TarExport) error {
	exclusionsMap := make(map[string]bool)
	for _, v := range exclusions {
		exclusionsMap[v] = true
	}

	dstDir := "documents"

	return filepath.Walk(DocumentsPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// First iteration, create the root folder
		if path == DocumentsPath {
			err = te.CreateDir(dstDir, info)
			if err != nil {
				return err
			}
			return nil
		}

		if _, found := exclusionsMap[path]; found {
			if info.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if _, found := ExcludedExtensions[filepath.Ext(path)]; found {
			log.Printf("WARN - Found file with interesting extension: %s\n", path)
		}

		rel, err := filepath.Rel(DocumentsPath, path)
		if err != nil {
			return err
		}

		destPath := filepath.Join(dstDir, rel)

		if info.IsDir() {
			err = te.CreateDir(destPath, info)
			if err != nil {
				return err
			}
		} else {
			err = te.AddFile(path, destPath, info)
			if err != nil {
				return err
			}
		}

		return nil
	})
}

func backup(docExc globExclusion, collExc exclusion) error {
	// Create temporary directory for export data
	log.Println("Creating temporary export directory...")
	dname, err := os.MkdirTemp(".", ".kindlebacker")
	if err != nil {
		return fmt.Errorf("Failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(dname)

	// Create temporary export file
	f, err := os.CreateTemp(dname, "export_file")
	if err != nil {
		return fmt.Errorf("Failed to create temporary export file: %w", err)
	}

	tarExport, err := NewTarExport(f)
	if err != nil {
		return fmt.Errorf("Failed to open tar export: %w", err)
	}

	// Export Content Catalogue DB data
	log.Println("Exporting content catalogue partial DB...")
	dbPath := filepath.Join(dname, "backer.partial")

	db, err := newCcDb(ROMode)
	if err != nil {
		log.Fatal(err)
	}
	defer db.close()

	err = db.exportData(dbPath, collExc, docExc)
	if err != nil {
		return err
	}

	// Add partial DB to export file
	dbInfo, err := os.Stat(dbPath)
	if err != nil {
		return fmt.Errorf("Failed to stat partial DB: %w", err)
	}

	err = tarExport.AddFile(dbPath, filepath.Base(dbPath), dbInfo)
	if err != nil {
		return fmt.Errorf("Failed to add partial DB to export: %w", err)
	}

	// Export the used thumbnails
	log.Println("Exporting thumbnails...")
	thumbnails, err := db.usedThumbnails()
	if err != nil {
		return fmt.Errorf("Failed to fetch thumbnails: %w", err)
	}

	err = addThumbnailsToTar(thumbnails, tarExport)
	if err != nil {
		return fmt.Errorf("Failed to add thumbnails to export: %w", err)
	}

	// err = copyThumbnails(thumbnails, dname)
	// if err != nil {
	// 	return fmt.Errorf("Failed to backup thumbnails: %w", err)
	// }

	// Export the documents folder (apply doc exclusions)
	log.Println("Exporting documents...")
	err = addDocumentsToTar(docExc, tarExport)
	if err != nil {
		return fmt.Errorf("Failed to add documents to export: %w", err)
	}

	// exclusionsMap := make(map[string]bool)
	// for _, v := range docExc {
	// 	exclusionsMap[v] = true
	// }

	// err = copyDir(DocumentsPath, dname, exclusionsMap)
	// if err != nil {
	// 	return fmt.Errorf("Failed to backup documents: %w", err)
	// }

	// Package the export
	log.Println("Readying the export...")
	currentTime := time.Now()
	exportFile := "kindle-backer_" + currentTime.Format(TimestampFormat) + ".tar.gz"

	// f, err := os.Create(exportFile)
	// if err != nil {
	// 	return fmt.Errorf("Failed to create export file: %w", err)
	// }

	// err = tarDirectoryContents(dname, f)
	// if err != nil {
	// 	if err = f.Close(); err != nil {
	// 		return fmt.Errorf("Failed to close export file: %w", err)
	// 	}
	// 	return fmt.Errorf("Failed to tar export directory: %w", err)
	// }

	if err = tarExport.Close(); err != nil {
		return fmt.Errorf("Failed to close tar export: %w", err)
	}

	if err = f.Close(); err != nil {
		return fmt.Errorf("Failed to close export file: %w", err)
	}

	err = os.Rename(f.Name(), exportFile)
	if err != nil {
		return fmt.Errorf("Failed to move the export file: %w", err)
	}

	log.Printf("Done exporting! Find it in %s\n", exportFile)

	return nil

	// f, err := os.CreateTemp("", "kbacker-export")
	// if err != nil {
	// 	return fmt.Errorf("Failed to create export file: %w", err)
	// }
	// defer os.Remove(f.Name())

	// err = tarDirectoryContents(dname, f)
	// if err != nil {
	// 	return fmt.Errorf("Failed to tar export directory: %w", err)
	// }

	// // Copy the export to the final destination with a timestamped name
	// log.Println("Copying the export to the final destination...")

	// err = copyFile(f.Name(), exportFile)
	// if err != nil {
	// 	return fmt.Errorf("Failed to copy the export file: %w", err)
	// }

	// log.Printf("Done exporting! Find it in %s\n", exportFile)

	// return nil
}

func enhanceDocumentsExclusion(docExc *globExclusion, useDefault bool) error {
	if useDefault == false {
		return nil
	}

	for _, exclusion := range DefaultExclusions {
		err := docExc.Set(exclusion)
		if err != nil {
			return err
		}
	}

	return nil
}

func main() {
	backupCmd := flag.NewFlagSet("backup", flag.ExitOnError)

	var documentsExclusion globExclusion
	backupCmd.Var(
		&documentsExclusion, "excludepath",
		"Glob path of documents to ignore. Can be called multiple times. Wrap in single quotes. "+
			"Used in combination with default paths unless disabled, see no-default-path-exclusions",
	)

	var collectionsExclusion exclusion
	backupCmd.Var(
		&collectionsExclusion, "excludecollection",
		"Case-insensitive match of collections to ignore. Can be called multiple times.",
	)

	noDefaultExclusions := backupCmd.Bool(
		"no-default-path-exclusions", false,
		"Disable default path exclusions. Use excludepath for custom paths",
	)

	restoreCmd := flag.NewFlagSet("restore", flag.ExitOnError)

	if len(os.Args) < 2 {
		log.Fatalf("Expected 'backup' or 'restore' subcommands")
	}

	var err error
	switch os.Args[1] {
	case "backup":
		backupCmd.Parse(os.Args[2:])
		err = enhanceDocumentsExclusion(&documentsExclusion, !(*noDefaultExclusions))
		if err != nil {
			break
		}

		err = backup(documentsExclusion, collectionsExclusion)
	case "restore":
		restoreCmd.Parse(os.Args[2:])
	default:
		log.Fatalf("Expected 'backup' or 'restore' subcommands")
	}

	if err != nil {
		log.Fatal(err)
	}
}

// func main() {
// 	flagSet := flag.NewFlagSet("kindle-backer", flag.ExitOnError)

// 	var exclusionFlag exclusion
// 	flagSet.Var(
// 		&exclusionFlag, "exclude",
// 		"Glob paths to ignore. Can be called multiple times. Wrap in single quotes",
// 	)

// 	flagSet.Usage = func() {
// 		fmt.Fprintf(flagSet.Output(), "Usage of %s:\n", os.Args[0])
// 		flagSet.PrintDefaults()
// 		fmt.Fprintf(flagSet.Output(), "Positional parameters: [directory (defaults to .)]\n")
// 	}

// 	fmt.Printf("os args %d %s\n", len(os.Args), os.Args)
// 	flagSet.Parse(os.Args[1:])

// 	fmt.Printf("Collected: %s\n", exclusionFlag)

// 	directory := flagSet.Arg(0)
// 	if directory == "" {
// 		directory = "."
// 	}

// 	directory, err := filepath.Abs(directory)
// 	if err != nil {
// 		log.Fatalf("Couldn't parse directory")
// 	}

// 	exclusionSet := map[string]bool{}
// 	for _, exclusion := range exclusionFlag {
// 		exclusionSet[exclusion] = true
// 	}
// 	filePaths(directory, exclusionSet)

// 	// for _, entry := range exclusionFlag {
// 	// 	matches, err := filepath.Glob(entry)
// 	// 	if err != nil {
// 	// 		log.Fatal(err)
// 	// 	}
// 	// 	fmt.Printf("Entry: %s, matches: %s\n", entry, matches)
// 	// }
// }
