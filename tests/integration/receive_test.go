//go:build unix

// Package integration runs the built deaddrop binary as a subprocess.
package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "deaddrop-it")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "deaddrop")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/deaddrop")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type proc struct {
	t      *testing.T
	cmd    *exec.Cmd
	events chan map[string]any
	exit   chan int
	link   string
	base   string
	token  string
	dir    string
}

func start(t *testing.T, extra ...string) *proc {
	t.Helper()
	dir := t.TempDir()
	args := append([]string{"receive", "--bind", "127.0.0.1", "--port", "0", "--dir", dir, "--json", "--json-include-secret"}, extra...)
	cmd := exec.Command(bin, args...)
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &proc{t: t, cmd: cmd, events: make(chan map[string]any, 100), exit: make(chan int, 1), dir: dir}
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			var ev map[string]any
			if json.Unmarshal(sc.Bytes(), &ev) == nil {
				p.events <- ev
			}
		}
		close(p.events)
	}()
	go func() {
		cmd.Wait()
		p.exit <- cmd.ProcessState.ExitCode()
	}()
	t.Cleanup(func() { cmd.Process.Kill() })

	ev := p.waitEvent("session_started")
	p.link = ev["link"].(string)
	p.base, p.token, _ = strings.Cut(p.link, "/r#")
	return p
}

func (p *proc) waitEvent(name string) map[string]any {
	p.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-p.events:
			if !ok {
				p.t.Fatalf("process exited before %s", name)
			}
			if ev["event"] == name {
				return ev
			}
		case <-timeout:
			p.t.Fatalf("timed out waiting for %s", name)
		}
	}
}

func (p *proc) waitExit() int {
	p.t.Helper()
	select {
	case c := <-p.exit:
		return c
	case <-time.After(15 * time.Second):
		p.t.Fatal("process did not exit")
		return -1
	}
}

func (p *proc) upload(name string, body io.Reader) int {
	p.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, p.base+"/api/upload", body)
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("X-File-Name", name)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return -1
	}
	resp.Body.Close()
	return resp.StatusCode
}

func sessionDirs(t *testing.T, base string) []string {
	t.Helper()
	es, _ := os.ReadDir(base)
	var out []string
	for _, e := range es {
		out = append(out, filepath.Join(base, e.Name()))
	}
	return out
}

func runExit(args ...string) (int, string) {
	cmd := exec.Command(bin, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	cmd.Run()
	return cmd.ProcessState.ExitCode(), buf.String()
}

func TestExitCodes(t *testing.T) {
	cases := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"--version"}, 0, "deaddrop "},
		{[]string{"--help"}, 0, "deaddrop receive"},
		{[]string{"receive", "--help"}, 0, "--max-file"},
		{nil, 2, "missing command"},
		{[]string{"send", "."}, 2, "v0.2"},
		{[]string{"receive", "--bogus"}, 2, "unknown flag --bogus"},
		{[]string{"receive", "--max-total", "5GB"}, 2, "binary units"},
		{[]string{"receive", "--expires", "48h"}, 2, "24h"},
	}
	for _, c := range cases {
		code, out := runExit(c.args...)
		if code != c.code || !strings.Contains(out, c.want) {
			t.Errorf("deaddrop %q: exit %d, output %q", c.args, code, out)
		}
	}
}

func TestPortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	code, out := runExit("receive", "--bind", "127.0.0.1", "--port", strconv.Itoa(port), "--dir", t.TempDir())
	if code != 3 || !strings.Contains(out, "already in use") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestUploadAndInterrupt(t *testing.T) {
	p := start(t, "--name", "Integration")
	if code := p.upload("hello.txt", strings.NewReader("hello world")); code != 201 {
		t.Fatalf("upload: %d", code)
	}
	saved := p.waitEvent("upload_saved")
	if saved["name"] != "hello.txt" || saved["size"].(float64) != 11 {
		t.Fatalf("%v", saved)
	}
	p.cmd.Process.Signal(os.Interrupt)
	ended := p.waitEvent("session_ended")
	if ended["reason"] != "ended" || ended["files"].(float64) != 1 || ended["bytes"].(float64) != 11 {
		t.Fatalf("%v", ended)
	}
	if code := p.waitExit(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	dirs := sessionDirs(t, p.dir)
	if len(dirs) != 1 {
		t.Fatalf("dirs %v", dirs)
	}
	b, err := os.ReadFile(filepath.Join(dirs[0], "hello.txt"))
	if err != nil || string(b) != "hello world" {
		t.Fatalf("%q %v", b, err)
	}
}

func TestInterruptDuringUploadRemovesPartial(t *testing.T) {
	p := start(t)
	pr, pw := io.Pipe()
	result := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, p.base+"/api/upload", pr)
		req.Header.Set("Authorization", "Bearer "+p.token)
		req.Header.Set("X-File-Name", "big.bin")
		req.ContentLength = 10 << 20
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			result <- -1
			return
		}
		resp.Body.Close()
		result <- resp.StatusCode
	}()
	pw.Write(make([]byte, 1<<20))
	p.waitEvent("upload_started")

	dirs := sessionDirs(t, p.dir)
	if len(dirs) != 1 {
		t.Fatalf("dirs %v", dirs)
	}
	p.cmd.Process.Signal(os.Interrupt)
	ended := p.waitEvent("session_ended")
	if ended["files"].(float64) != 0 {
		t.Fatalf("%v", ended)
	}
	if code := p.waitExit(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	pw.Close()
	<-result
	if _, err := os.Stat(dirs[0]); !os.IsNotExist(err) {
		es, _ := os.ReadDir(dirs[0])
		t.Fatalf("session dir should be gone, has %v", es)
	}
}

func TestExpiry(t *testing.T) {
	p := start(t, "--expires", "1s")
	ended := p.waitEvent("session_ended")
	if ended["reason"] != "expired" {
		t.Fatalf("%v", ended)
	}
	if code := p.waitExit(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if code := p.upload("late.txt", strings.NewReader("x")); code != -1 && code != 403 {
		t.Fatalf("upload after expiry: %d", code)
	}
}

func TestJSONOmitsSecretByDefault(t *testing.T) {
	cmd := exec.Command(bin, "receive", "--bind", "127.0.0.1", "--port", "0", "--dir", t.TempDir(), "--json", "--expires", "1s")
	out, _ := cmd.Output()
	if !bytes.Contains(out, []byte(`"session_started"`)) {
		t.Fatalf("no start event: %s", out)
	}
	if bytes.Contains(out, []byte("/r#")) || bytes.Contains(out, []byte(`"link"`)) {
		t.Fatalf("secret printed: %s", out)
	}
}
