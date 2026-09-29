// Package session is the Sunshine prep-cmd protocol: `vos session
// begin|end` (run by Sunshine as the gaming user) talks to vosd over
// /run/vos/session.sock. See docs/CONTRACTS.md "Session protocol".
package session

import (
	"context"
	"fmt"
	"os"
)

type Request struct {
	Op     string `json:"op"` // "begin" | "end"
	Client string `json:"client,omitempty"`
	App    string `json:"app,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	FPS    int    `json:"fps,omitempty"`
	HDR    bool   `json:"hdr,omitempty"`
}

type Response struct {
	OK      bool   `json:"ok"`
	Mode    string `json:"mode,omitempty"`
	HDR     bool   `json:"hdr,omitempty"`
	Message string `json:"message,omitempty"`
}

// Handler is implemented by the display manager in vosd.
type Handler interface {
	Begin(ctx context.Context, req Request) Response
	End(ctx context.Context)
}

// Serve listens on path (mode 0660, group of the gaming user) until ctx ends.
func Serve(ctx context.Context, path string, h Handler) error { <-ctx.Done(); return nil }

// CLI implements `vos session begin|end`. It always exits 0.
func CLI(args []string) int {
	fmt.Fprintln(os.Stderr, "vos session: not implemented")
	return 0
}
