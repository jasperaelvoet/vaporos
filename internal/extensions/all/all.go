// Package all links every extension's helper into vos: each helper package
// (internal/extensions/<id>) registers itself from its init function, and
// cmd/vos imports this package for its side effects.
package all
