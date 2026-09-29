// This stub module only fences tools/web/ off from the root module, so that
// `go list ./...`, `go vet ./...` and `go test ./...` at the repo root never
// walk into tools/web/node_modules (some npm packages ship stray .go files).
// Nothing here is Go code.
module github.com/jasperaelvoet/vaporos/tools/web

go 1.24.0
