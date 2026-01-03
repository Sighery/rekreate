package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

type TarExport struct {
	gzWriter  *gzip.Writer
	tarWriter *tar.Writer
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
