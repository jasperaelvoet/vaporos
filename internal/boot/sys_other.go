//go:build !linux

package boot

// Off Linux (the dev Mac runs only the tests) the ESP is a temp dir.

func renameNoReplace(src, dst string) error { return renameIfAbsent(src, dst) }

func clearImmutable(string) {}
