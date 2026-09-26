package sshx

import (
	"io"

	"golang.org/x/crypto/ssh"
)

// Shell is an interactive login shell on a pseudo-terminal, for a
// terminal the user types into.
type Shell struct {
	sess  *ssh.Session
	stdin io.WriteCloser
	// Output is everything the shell prints (the terminal merges stdout
	// and stderr). It must be read continuously, or the shell stalls; it
	// ends when the shell does.
	Output io.Reader
	done   chan struct{}
	err    error
}

// Shell starts a login shell on a cols x rows pseudo-terminal.
func (c *Client) Shell(cols, rows int) (*Shell, error) {
	sess, err := c.conn.NewSession()
	if err != nil {
		return nil, err
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 115200, ssh.TTY_OP_OSPEED: 115200}
	if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		sess.Close()
		return nil, err
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	pr, pw := io.Pipe()
	sess.Stdout, sess.Stderr = pw, pw
	if err := sess.Shell(); err != nil {
		sess.Close()
		return nil, err
	}
	sh := &Shell{sess: sess, stdin: stdin, Output: pr, done: make(chan struct{})}
	go func() {
		sh.err = sess.Wait()
		pw.Close()
		close(sh.done)
	}()
	return sh, nil
}

// Write types into the terminal.
func (s *Shell) Write(p []byte) (int, error) { return s.stdin.Write(p) }

// Resize tells the server the terminal's new size.
func (s *Shell) Resize(cols, rows int) error { return s.sess.WindowChange(rows, cols) }

// Close ends the shell.
func (s *Shell) Close() error { return s.sess.Close() }

// Done is closed when the shell has ended.
func (s *Shell) Done() <-chan struct{} { return s.done }

// Err is why the shell ended, once Done is closed: nil for a normal exit.
func (s *Shell) Err() error { return s.err }
