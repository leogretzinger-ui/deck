//go:build !unix

package main

import "errors"

func execPreview(string, []string, []string) error {
	return errors.New("the project preview launcher requires a Unix host")
}
