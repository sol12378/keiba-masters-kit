package voting

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A deep checkout pushes the control socket past sockaddr_un's 104-byte
// sun_path and bind() answers with a bare "invalid argument", which reads like
// a permissions problem and is not one.  The listener should name the real
// cause before anyone goes looking in the wrong place.
func TestControlSocketPathOverThePlatformLimitIsExplained(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	deep := filepath.Join(root, strings.Repeat("a", 120), "votingd.sock")
	_, err = ListenControlSocket(deep)
	if err == nil {
		t.Fatal("expected an over-long socket path to be refused")
	}
	for _, want := range []string{"control socket path", "platform limit", "shorter"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error does not mention %q: %v", want, err)
		}
	}
}

func TestControlSocketPathWithinTheLimitIsAccepted(t *testing.T) {
	// t.TempDir() is already too deep on macOS, which is the whole point of
	// the check above, so this needs a deliberately short directory.
	root, err := os.MkdirTemp("", "vsock")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	path := filepath.Join(root, "s.sock")
	if len(path) >= maxUnixSocketPath {
		t.Skipf("the platform temporary directory is itself too deep: %s", path)
	}
	listener, err := ListenControlSocket(path)
	if err != nil {
		t.Fatalf("ListenControlSocket: %v", err)
	}
	defer listener.Close()
}
