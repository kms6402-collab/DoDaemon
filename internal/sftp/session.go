package sftp

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/kms6402/dodaemon/internal/auth"
	"github.com/kms6402/dodaemon/internal/config"
	"github.com/kms6402/dodaemon/internal/eventbus"
)

// handleConn performs the SSH handshake for one TCP connection, then
// serves every "session" channel that requests the "sftp" subsystem —
// exactly what an SFTP-only (no shell, no exec, no port-forwarding)
// SSH server needs to do. One client can open multiple channels; each
// gets its own sftp.RequestServer scoped to the authenticated user.
func (s *Server) handleConn(ctx context.Context, conn net.Conn, cfg *ssh.ServerConfig, store *auth.Store) {
	remote := conn.RemoteAddr().String()

	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		conn.Close()
		return // failed handshake/auth already logged via PasswordCallback
	}
	defer sshConn.Close()

	s.publish(eventbus.KindConnect, remote, fmt.Sprintf("SFTP 연결됨: %s", sshConn.User()), nil)
	defer s.publish(eventbus.KindDisconnect, remote, fmt.Sprintf("SFTP 연결 종료: %s", sshConn.User()), nil)

	go ssh.DiscardRequests(reqs)

	username := sshConn.User()
	u, ok := userByName(s.cfg.Users, username)
	if !ok {
		return // authenticated by store but vanished from config between auth and here — treat as no access
	}

	var wg sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			return
		case newChannel, open := <-chans:
			if !open {
				wg.Wait()
				return
			}
			if newChannel.ChannelType() != "session" {
				newChannel.Reject(ssh.UnknownChannelType, "only sftp sessions are supported")
				continue
			}
			channel, requests, err := newChannel.Accept()
			if err != nil {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.serveSession(channel, requests, remote, u)
			}()
		}
	}
}

// serveSession waits for the client's "subsystem sftp" request (the only
// thing this SSH server supports — no shell, no exec) and then hands the
// channel to pkg/sftp's request server, backed by fs.go's jailed,
// permission-checked handlers.
func (s *Server) serveSession(channel ssh.Channel, requests <-chan *ssh.Request, remote string, u config.FTPUser) {
	defer channel.Close()

	subsystemRequested := make(chan bool, 1)
	go func() {
		for req := range requests {
			ok := req.Type == "subsystem" && subsystemName(req.Payload) == "sftp"
			if req.WantReply {
				req.Reply(ok, nil)
			}
			if ok {
				subsystemRequested <- true
			}
		}
	}()

	if !<-subsystemRequested {
		return
	}

	handlers := s.newHandlers(remote, u)
	rs := sftp.NewRequestServer(channel, handlers)
	defer rs.Close()
	rs.Serve() //nolint:errcheck // client hangup is the normal end of a session, nothing to report
}

func subsystemName(payload []byte) string {
	if len(payload) < 4 {
		return ""
	}
	n := int(payload[0])<<24 | int(payload[1])<<16 | int(payload[2])<<8 | int(payload[3])
	if n < 0 || 4+n > len(payload) {
		return ""
	}
	return string(payload[4 : 4+n])
}

func userByName(users []config.FTPUser, name string) (config.FTPUser, bool) {
	for _, u := range users {
		if u.Username == name {
			return u, true
		}
	}
	return config.FTPUser{}, false
}
