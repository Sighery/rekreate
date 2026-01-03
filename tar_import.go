package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

type TarImport struct {
	tarPath string
}

func NewTarImport(file string) *TarImport {
	return &TarImport{
		tarPath: file,
	}
}

func (t *TarImport) ExtractFile(src, dst string) error {
	tarFile, err := os.Open(t.tarPath)
	if err != nil {
		return err
	}
	defer tarFile.Close()

	gz, err := gzip.NewReader(tarFile)
	if err != nil {
		return err
	}
	defer gz.Close()

	tw := tar.NewReader(gz)

	for {
		header, err := tw.Next()
		if err == io.EOF {
			return fmt.Errorf("File %s not found on the backup", src)
		}
		if err != nil {
			return fmt.Errorf("Failed to read tar entry: %w", err)
		}

		if header == nil {
			log.Println("WARN - Ignoring empty tar header")
			continue
		}

		if header.Name != src {
			continue
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		target := filepath.Join(dst, header.Name)

		f, err := os.Create(target)
		if err != nil {
			return fmt.Errorf("Failed to open partial file for write: %w", err)
		}

		if _, err := io.Copy(f, tw); err != nil {
			return fmt.Errorf("Failed to copy partial file: %w", err)
		}

		if err := f.Close(); err != nil {
			return fmt.Errorf("Failed to close partial file: %w", err)
		}

		return nil
	}

	return fmt.Errorf("File %s not found on the backup", src)
}

func (t *TarImport) ExtractPartialDb(dst string) (string, error) {
	if err := t.ExtractFile("backer.partial", dst); err != nil {
		return "", err
	}

	return filepath.Join(dst, "backer.partial"), nil
}

func (t *TarImport) ExtractThumbnails() error {
	tarFile, err := os.Open(t.tarPath)
	if err != nil {
		return err
	}
	defer tarFile.Close()

	gz, err := gzip.NewReader(tarFile)
	if err != nil {
		return err
	}
	defer gz.Close()

	tw := tar.NewReader(gz)

	for {
		header, err := tw.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("Failed to read tar entry: %w", err)
		}

		if header == nil {
			log.Println("WARN - Ignoring empty tar header")
			continue
		}

		if strings.HasPrefix(header.Name, "thumbnails/") == false {
			continue
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		trimmed, _ := strings.CutPrefix(header.Name, "thumbnails/")
		target := filepath.Join(ThumbnailsPath, trimmed)

		f, err := os.Create(target)
		if err != nil {
			return fmt.Errorf("Failed to open partial file for write: %w", err)
		}

		if _, err := io.Copy(f, tw); err != nil {
			return fmt.Errorf("Failed to copy partial file: %w", err)
		}

		if err := f.Close(); err != nil {
			return fmt.Errorf("Failed to close partial file: %w", err)
		}
	}

	return nil
}

func (t *TarImport) ExtractDocuments() error {
	tarFile, err := os.Open(t.tarPath)
	if err != nil {
		return err
	}
	defer tarFile.Close()

	gz, err := gzip.NewReader(tarFile)
	if err != nil {
		return err
	}
	defer gz.Close()

	tw := tar.NewReader(gz)

	for {
		header, err := tw.Next()
		if err == io.EOF {
			return nil
		}

		if err != nil {
			return fmt.Errorf("Failed to read tar entry: %w", err)
		}

		if header == nil {
			log.Println("WARN - Ignoring empty tar header")
			continue
		}

		if strings.HasPrefix(header.Name, "documents/") == false {
			continue
		}

		trimmed, _ := strings.CutPrefix(header.Name, "documents/")
		target := filepath.Join(DocumentsPath, trimmed)

		switch header.Typeflag {
		case tar.TypeDir:
			if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
				if err := os.MkdirAll(target, 0755); err != nil {
					return fmt.Errorf(
						"Failed to create documents directories %s: %w", target, err,
					)
				}
			}
		case tar.TypeReg:
			f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("Failed to open the target file for write %s: %w", target, err)
			}

			if _, err := io.Copy(f, tw); err != nil {
				return fmt.Errorf("Failed to copy data to target %s: %w", target, err)
			}

			if err := f.Close(); err != nil {
				return fmt.Errorf("Failed to close target %s: %w", target, err)
			}
		}
	}

	return nil
}
