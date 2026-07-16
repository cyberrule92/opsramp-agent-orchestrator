package deploy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startTestSSHServer spins up a minimal in-process SSH server on localhost that
// accepts password auth for (user, pass) and executes exec requests: the upload
// command consumes stdin; any other command emits canned output and exits 0.
func startTestSSHServer(t *testing.T, user, pass string) (host string, port int, uploaded *string) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
			if c.User() == user && string(p) == pass {
				return &ssh.Permissions{}, nil
			}
			return nil, io.EOF
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	captured := ""
	uploaded = &captured

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveConn(conn, cfg, uploaded)
		}
	}()

	h, p, _ := net.SplitHostPort(ln.Addr().String())
	pn, _ := strconv.Atoi(p)
	return h, pn, uploaded
}

func serveConn(nConn net.Conn, cfg *ssh.ServerConfig, uploaded *string) {
	sshConn, chans, reqs, err := ssh.NewServerConn(nConn, cfg)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			newCh.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go func() {
			for req := range chReqs {
				if req.Type != "exec" {
					req.Reply(false, nil)
					continue
				}
				// exec payload: 4-byte length + command string.
				cmd := string(req.Payload[4:])
				req.Reply(true, nil)
				if strings.Contains(cmd, "cat >") {
					data, _ := io.ReadAll(ch) // consume uploaded script
					*uploaded = string(data)
				} else {
					io.WriteString(ch, "OpsRamp agent installed\n")
				}
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{0}))
				ch.Close()
			}
		}()
	}
}

func TestInstallOnHostEndToEnd(t *testing.T) {
	host, port, uploaded := startTestSSHServer(t, "tester", "pw")

	runner, err := NewRunner(t.TempDir() + "/known_hosts")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	outcome := runner.InstallOnHost(ctx, host, Credentials{
		User: "tester", Password: "pw", Port: port, UseSudo: false,
	}, InstallParams{
		APIHost: "host.api.opsramp.com", Key: "K", Secret: "S",
		Script: []byte("#!/bin/sh\necho hi\n"),
	})

	if !outcome.OK {
		t.Fatalf("expected success, got err=%q output=%q", outcome.Err, outcome.Output)
	}
	if outcome.ExitCode != 0 {
		t.Errorf("exit code = %d", outcome.ExitCode)
	}
	if !strings.Contains(outcome.Output, "OpsRamp agent installed") {
		t.Errorf("output missing install marker: %q", outcome.Output)
	}
	if !strings.Contains(*uploaded, "#!/bin/sh") {
		t.Errorf("installer script was not uploaded: %q", *uploaded)
	}
}

func TestInstallOnHostBadAuth(t *testing.T) {
	host, port, _ := startTestSSHServer(t, "tester", "pw")
	runner, _ := NewRunner(t.TempDir() + "/known_hosts")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	outcome := runner.InstallOnHost(ctx, host, Credentials{
		User: "tester", Password: "WRONG", Port: port,
	}, InstallParams{Script: []byte("x")})

	if outcome.OK {
		t.Fatal("expected auth failure")
	}
	if outcome.Err == "" {
		t.Error("expected error message on auth failure")
	}
}
