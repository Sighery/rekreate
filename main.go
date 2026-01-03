package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
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

	// Export the documents folder (apply doc exclusions)
	log.Println("Exporting documents...")
	err = addDocumentsToTar(docExc, tarExport)
	if err != nil {
		return fmt.Errorf("Failed to add documents to export: %w", err)
	}

	// Package the export
	log.Println("Readying the export...")
	currentTime := time.Now()
	exportFile := "kindle-backer_" + currentTime.Format(TimestampFormat) + ".tar.gz"

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

func restore(documents, db, thumbnails bool, exportPath string) error {
	// Create temporary directory for import data
	log.Println("Creating temporary import directory...")
	dname, err := os.MkdirTemp(".", ".kindlebacker")
	if err != nil {
		return fmt.Errorf("Failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(dname)

	// Open backup tar for import
	log.Println("Opening backup file...")
	tarImport := NewTarImport(exportPath)

	currentTime := time.Now()

	// Extract partial DB file
	if db == true {
		log.Println("Extracting partial DB...")
		partialPath, err := tarImport.ExtractPartialDb(dname)
		if err != nil {
			return fmt.Errorf("Failed to extract partial DB: %w", err)
		}

		log.Println("Backing up existing content catalogue DB...")
		err = copyFile(CcDb, CcDb+currentTime.Format(TimestampFormat)+".bak")
		if err != nil {
			return fmt.Errorf("Failed to back up the content catalogue DB: %w", err)
		}

		// Opening content catalogue DB
		log.Println("Connecting to content catalogue DB...")
		db, err := newCcDb(RWMode)
		if err != nil {
			return fmt.Errorf("Failed to open the content catalogue DB: %w", err)
		}
		defer db.close()

		log.Println("Importing content catalogue data...")
		if err = db.importData(partialPath); err != nil {
			return fmt.Errorf("Failed to import partial DB data: %w", err)
		}
	}

	// Extract thumbnails
	if thumbnails == true {
		log.Println("Importing thumbnails...")
		if err := tarImport.ExtractThumbnails(); err != nil {
			return fmt.Errorf("Failed to import thumbnails: %w", err)
		}
	}

	// Extract documents
	if documents == true {
		log.Println("Importing documents...")
		if err := tarImport.ExtractDocuments(); err != nil {
			return fmt.Errorf("Failed to import documents: %w", err)
		}
	}

	log.Println(
		"Done importing! Give it some seconds for all the content to appear in the home screen",
	)

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
	noDocuments := restoreCmd.Bool("no-documents", false, "Don't import documents")
	noDb := restoreCmd.Bool("no-db", false, "Don't import the content catalogue data")
	noThumbnails := restoreCmd.Bool("no-thumbnails", false, "Don't import thumbnails")

	restoreCmd.Usage = func() {
		fmt.Fprintf(restoreCmd.Output(), "Usage of restore:\n")
		restoreCmd.PrintDefaults()
		fmt.Fprintf(restoreCmd.Output(), "Positional parameters: exportFilePath")
	}

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

		exportFile := restoreCmd.Arg(0)
		if exportFile == "" {
			log.Fatal("Expected a positional argument with the export file")
		}
		if _, err = os.Stat(exportFile); err != nil {
			log.Fatal("Expected a positional argument with the export file")
		}

		err = restore(!(*noDocuments), !(*noDb), !(*noThumbnails), exportFile)
	case "help", "--help", "-help":
		backupCmd.Usage()
		fmt.Println()
		restoreCmd.Usage()
	default:
		log.Fatalf("Expected 'backup' or 'restore' subcommands")
	}

	if err != nil {
		log.Fatal(err)
	}
}
