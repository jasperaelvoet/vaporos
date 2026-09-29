// Package web is the embedded web UI: server-rendered page shells plus
// vanilla JS that calls /api/v1. No external assets.
package web

import "github.com/jasperaelvoet/vaporos/internal/api"

// Register adds pages and static assets to srv.
func Register(srv *api.Server) {}
