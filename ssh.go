package liveshare

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

type sshSession struct {
	*ssh.Session
	session *session
	socket  net.Conn
	conn    ssh.Conn
	reader  io.Reader
	writer  io.Writer
}

func newSSH(session *session, socket net.Conn) *sshSession {
	return &sshSession{session: session, socket: socket}
}

func (s *sshSession) connect(ctx context.Context) error {
	clientConfig := ssh.ClientConfig{
		User: "",
		Auth: []ssh.AuthMethod{
			ssh.Password(s.session.workspaceAccess.SessionToken),
		},
		HostKeyAlgorithms: []string{"rsa-sha2-512", "rsa-sha2-256"},
		HostKeyCallback:   ssh.InsecureIgnoreHostKey(),
		Timeout:           10 * time.Second,
	}

	sshClientConn, chans, reqs, err := ssh.NewClientConn(s.socket, "", &clientConfig)
	if err != nil {
		return fmt.Errorf("error creating ssh client connection: %v", err)
	}
	s.conn = sshClientConn

	sshClient := ssh.NewClient(sshClientConn, chans, reqs)
	s.Session, err = sshClient.NewSession()
	if err != nil {
		return fmt.Errorf("error creating ssh client session: %v", err)
	}

	s.reader, err = s.Session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("error creating ssh session reader: %v", err)
	}

	s.writer, err = s.Session.StdinPipe()
	if err != nil {
		return fmt.Errorf("error creating ssh session writer: %v", err)
	}

	return nil
}

func (s *sshSession) Read(p []byte) (n int, err error) {
	return s.reader.Read(p)
}

func (s *sshSession) Write(p []byte) (n int, err error) {
	return s.writer.Write(p)
}
