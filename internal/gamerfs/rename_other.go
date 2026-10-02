//go:build !unix

package gamerfs

func rename(string, []string, string) error { return errUnsupported }
