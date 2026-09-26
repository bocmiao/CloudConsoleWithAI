// Package sshtest runs an in-process SSH server for tests. Exec requests
// run with the local sh, so scripts can be tested end to end; shell
// requests start the local ShellCommand, and the sftp subsystem serves the
// local files.
package sshtest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Server is a password-authenticated SSH server on 127.0.0.1.
type Server struct {
	Host    string
	Port    int
	HostKey string // SHA256 fingerprint of the server's host key
	// ClientKey is a private key (OpenSSH PEM) the server accepts for the
	// user, for testing key logins.
	ClientKey string
	// ShellCommand runs for shell requests; its output goes back as the
	// terminal's. Default: sh -i. It does not get a real pseudo-terminal.
	ShellCommand []string
	cfg          *ssh.ServerConfig
	ln           net.Listener

	mu    sync.Mutex
	sizes []string // "TERM cols x rows" from pty-req, then "cols x rows" per window-change
}

// Sizes lists the terminal sizes clients asked for, in order.
func (s *Server) Sizes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sizes...)
}

func (s *Server) noteSize(v string) {
	s.mu.Lock()
	s.sizes = append(s.sizes, v)
	s.mu.Unlock()
}

// TB is the part of testing.TB that Start needs.
type TB interface {
	Helper()
	Fatal(args ...any)
	Cleanup(func())
}

// Start listens on a random port; it stops when the test ends.
func Start(t TB, user, password string) *Server {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	clientPub, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorized, _ := ssh.NewPublicKey(clientPub)
	block, err := ssh.MarshalPrivateKey(clientPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
			if c.User() == user && string(p) == password {
				return nil, nil
			}
			return nil, fmt.Errorf("password rejected for %q", c.User())
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if c.User() == user && bytes.Equal(k.Marshal(), authorized.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("key rejected for %q", c.User())
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	s := &Server{Host: host, Port: p, HostKey: ssh.FingerprintSHA256(signer.PublicKey()), ClientKey: string(pem.EncodeToMemory(block)), cfg: cfg, ln: ln}
	go s.serve()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *Server) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *Server) handle(nc net.Conn) {
	_, chans, reqs, err := ssh.NewServerConn(nc, s.cfg)
	if err != nil {
		nc.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() != "session" {
			_ = nch.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, requests, err := nch.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			env := os.Environ()
			for req := range requests {
				switch req.Type {
				case "pty-req":
					var pty struct {
						Term          string
						Cols, Rows    uint32
						Width, Height uint32
						Modes         string
					}
					if err := ssh.Unmarshal(req.Payload, &pty); err == nil {
						s.noteSize(fmt.Sprintf("%s %dx%d", pty.Term, pty.Cols, pty.Rows))
						env = append(env, "TERM="+pty.Term)
					}
					_ = req.Reply(true, nil)
					continue
				case "window-change":
					var w struct{ Cols, Rows, Width, Height uint32 }
					if err := ssh.Unmarshal(req.Payload, &w); err == nil {
						s.noteSize(fmt.Sprintf("%dx%d", w.Cols, w.Rows))
					}
					continue
				case "shell":
					_ = req.Reply(true, nil)
					argv := s.ShellCommand
					if len(argv) == 0 {
						argv = []string{"sh", "-i"}
					}
					cmd := exec.Command(argv[0], argv[1:]...)
					cmd.Env = env
					cmd.Stdout, cmd.Stderr = ch, ch
					// The client keeps stdin open; copying it ourselves
					// lets the shell's own exit end the session.
					stdin, err := cmd.StdinPipe()
					if err != nil {
						return
					}
					go func() { _, _ = io.Copy(stdin, ch); stdin.Close() }()
					go func() {
						// Keep answering window-change and the like.
						for req := range requests {
							if req.Type == "window-change" {
								var w struct{ Cols, Rows, Width, Height uint32 }
								if err := ssh.Unmarshal(req.Payload, &w); err == nil {
									s.noteSize(fmt.Sprintf("%dx%d", w.Cols, w.Rows))
								}
							} else if req.WantReply {
								_ = req.Reply(false, nil)
							}
						}
					}()
					_ = cmd.Run()
					code := 0
					if cmd.ProcessState != nil {
						code = cmd.ProcessState.ExitCode()
					}
					_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
					return
				}
				if req.Type == "subsystem" {
					var sub struct{ Name string }
					if err := ssh.Unmarshal(req.Payload, &sub); err != nil || sub.Name != "sftp" {
						_ = req.Reply(false, nil)
						continue
					}
					_ = req.Reply(true, nil)
					if srv, err := sftp.NewServer(ch); err == nil {
						_ = srv.Serve()
					}
					return
				}
				if req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
				var payload struct{ Command string }
				if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
					_ = req.Reply(false, nil)
					return
				}
				_ = req.Reply(true, nil)
				cmd := exec.Command("sh", "-c", payload.Command)
				cmd.Stdin, cmd.Stdout, cmd.Stderr = ch, ch, ch.Stderr()
				code := 0
				if err := cmd.Run(); err != nil {
					var ee *exec.ExitError
					if errors.As(err, &ee) {
						code = ee.ExitCode()
					} else {
						code = 127
					}
				}
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
				return
			}
		}()
	}
}
