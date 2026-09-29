// Package brand carries the VaporOS design tokens into Go: the state
// vocabulary and its styles, the palette and heat ramps, the TV's type and
// look, the screen-shape readout (ModeLabel) and the TV-to-phone handshake
// mark.
//
// design/tokens.json is the only source. tokens_gen.go and every other
// generated output (the control center's tokens.css, the website's
// tokens.gen.css and tokens.gen.ts) are written by a test, so go build never
// runs the generator:
//
//	VOS_GEN_DESIGN=1 go test ./internal/brand -run TestGenerateDesign
//
// TestDesignOutputsFresh fails when a committed output no longer matches
// tokens.json. design/README.md describes the schema and the targets.
//
// brand imports nothing from this repository, so any package may import it.
package brand
