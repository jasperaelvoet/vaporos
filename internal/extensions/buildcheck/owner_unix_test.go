//go:build unix

package buildcheck

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestOwner(t *testing.T) {
	fi, err := os.Lstat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := owner(fi); !strings.HasPrefix(got, fmt.Sprintf("%d:", os.Geteuid())) {
		t.Fatalf("owner %q, want uid %d", got, os.Geteuid())
	}
}
