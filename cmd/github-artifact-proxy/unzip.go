package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func Unzip(r *zip.ReadCloser, destDir string) error {
	for _, f := range r.File {
		if err := extractFile(f, destDir); err != nil {
			return err
		}
	}

	return nil
}

func extractFile(f *zip.File, destDir string) error {
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()

	path := filepath.Join(destDir, f.Name)
	if !strings.HasPrefix(path, filepath.Clean(destDir)+string(os.PathSeparator)) {
		return fmt.Errorf("attempt to write outside of destination directory: %s", path)
	}

	if f.FileInfo().IsDir() {
		if err := os.MkdirAll(path, f.Mode()); err != nil {
			return err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(path), f.Mode()); err != nil {
			return err
		}

		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}
		defer f.Close()

		if _, err = io.Copy(f, r); err != nil {
			return err
		}
	}

	return nil
}
