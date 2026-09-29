package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHealthSerialLine(t *testing.T) {
	HealthSerial = filepath.Join(t.TempDir(), "ttyS0")
	if err := os.WriteFile(HealthSerial, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	healthSerial("degraded", []string{"no GPU\n(amdgpu)", "stream down"})
	healthSerial("ok", nil)
	b, _ := os.ReadFile(HealthSerial)
	want := "VOS-HEALTH result=degraded failures=no GPU (amdgpu); stream down\nVOS-HEALTH result=ok failures=-\n"
	if string(b) != want {
		t.Errorf("serial got %q, want %q", b, want)
	}
}
