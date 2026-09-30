package web

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// The control center has two fakes of vosd over one set of fixtures: the
// dev server's (TestDevServer: the real api core with the fake services of
// devserver_*_test.go), which the e2e harness uses, and the website demo's
// (demo/engine.js), which runs in the browser.

const engineVectorsDir = "fixtures/vectors"

// TestDemoEngine runs fixtures/vectors/engine.test.mjs: what only the JS
// engine has (the transport the demo installs as globalThis.vosTransport,
// and save/restore across page loads).
func TestDemoEngine(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	out, err := exec.Command(node, "--test", filepath.Join(engineVectorsDir, "engine.test.mjs")).CombinedOutput()
	if err != nil {
		t.Errorf("node --test engine.test.mjs: %v\n%s", err, out)
	}
}
