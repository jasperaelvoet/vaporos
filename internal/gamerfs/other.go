//go:build !unix

package gamerfs

import (
	"errors"
	"os"
)

// VaporOS is Linux; this keeps the package compiling on other systems.

var errUnsupported = errors.New("gamerfs: not supported on this operating system")

func openFile(string, []string) (*os.File, error)                      { return nil, errUnsupported }
func mkdirAll(string, []string, mkdirSpec) error                       { return errUnsupported }
func writeFile(string, []string, []byte, os.FileMode, mkdirSpec) error { return errUnsupported }
