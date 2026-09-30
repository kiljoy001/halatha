//go:build !js && !wasm

package wisp

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"tractor.dev/wanix"
	"tractor.dev/wanix/fs"
)

// TestListenURL pins the URL advertised to the browser, including the
// wildcard-bind rewrites that make the printed URL actually dialable.
func TestListenURL(t *testing.T) {
	cases := []struct{ bind, want string }{
		{"127.0.0.1:7655", "wisp://127.0.0.1:7655"},
		{"0.0.0.0:7655", "wisp://localhost:7655"},
		{"[::]:9000", "wisp://localhost:9000"},
		{"192.168.1.5:1234", "wisp://192.168.1.5:1234"},
	}
	for _, tc := range cases {
		ln := &fakeListener{addr: fakeAddr(tc.bind)}
		if got := listenURL(ln); got != tc.want {
			t.Errorf("listenURL(%s) = %s, want %s", tc.bind, got, tc.want)
		}
	}
}

// TestListenURLMalformedAddr pins the fallback when the bound address cannot
// be split, so the relay never prints an empty or malformed URL.
func TestListenURLMalformedAddr(t *testing.T) {
	ln := &fakeListener{addr: fakeAddr("not-a-host-port")}
	if got := listenURL(ln); got != "wisp://localhost:7655" {
		t.Errorf("listenURL(malformed) = %s, want the default fallback", got)
	}
}

type fakeAddr string

func (f fakeAddr) Network() string { return "tcp" }
func (f fakeAddr) String() string  { return string(f) }

type fakeListener struct{ addr net.Addr }

func (f *fakeListener) Accept() (net.Conn, error) { return nil, nil }
func (f *fakeListener) Close() error              { return nil }
func (f *fakeListener) Addr() net.Addr            { return f.addr }

// newTestTask allocates a real wisp task without starting it, so the
// driver's pure helpers can be exercised against genuine Task state.
func newTestTask(t *testing.T, cmd string, env []string) *wanix.Task {
	t.Helper()
	taskfs := wanix.NewTaskFS()
	taskfs.Register("wisp", &Driver{})
	root, err := wanix.NewRootWithTasks(taskfs)
	if err != nil {
		t.Fatal(err)
	}
	task, err := taskfs.Alloc("wisp", root)
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "" {
		f, err := task.Open("cmd")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fs.Write(f, []byte(cmd)); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if len(env) > 0 {
		f, err := task.Open("env")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fs.Write(f, []byte(strings.Join(env, "\n"))); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	return task
}

// TestResolveListenAddr pins the listen-address precedence documented on
// Driver.Start: task arg, then WISP_LISTEN, then the default.
func TestResolveListenAddr(t *testing.T) {
	t.Run("default when nothing set", func(t *testing.T) {
		task := newTestTask(t, "", nil)
		if got := resolveListenAddr(task); got != DefaultListen {
			t.Errorf("got %q, want %q", got, DefaultListen)
		}
	})

	t.Run("env overrides default", func(t *testing.T) {
		task := newTestTask(t, "", []string{"WISP_LISTEN=:9999"})
		if got := resolveListenAddr(task); got != ":9999" {
			t.Errorf("got %q, want :9999", got)
		}
	})

	t.Run("arg wins over env", func(t *testing.T) {
		task := newTestTask(t, ":8888", []string{"WISP_LISTEN=:9999"})
		if got := resolveListenAddr(task); got != ":8888" {
			t.Errorf("got %q, want :8888", got)
		}
	})

	t.Run("unrelated env ignored", func(t *testing.T) {
		task := newTestTask(t, "", []string{"WISP_LISTENER=:1", "OTHER=x"})
		if got := resolveListenAddr(task); got != DefaultListen {
			t.Errorf("got %q, want %q", got, DefaultListen)
		}
	})
}

// TestRecordExit pins the exit code written to #task/<id>/exit: 0 for a clean
// shutdown (including http.ErrServerClosed) and 1 for a real serve failure.
func TestRecordExit(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil is success", nil, "0"},
		{"ErrServerClosed is success", http.ErrServerClosed, "0"},
		{"real error is failure", errors.New("listen failed"), "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := newTestTask(t, "", nil)
			d := &Driver{}
			d.recordExit(task, tc.err)

			f, err := task.Open("exit")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			b, err := io.ReadAll(f)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(b)); got != tc.want {
				t.Errorf("exit = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDriverCheckIsAlwaysFalse pins that wisp is never auto-detected.
func TestDriverCheckIsAlwaysFalse(t *testing.T) {
	d := &Driver{}
	if d.Check(nil) {
		t.Error("Check(nil) = true, want false")
	}
	if d.Check(newTestTask(t, "", nil)) {
		t.Error("Check(task) = true, want false")
	}
}
