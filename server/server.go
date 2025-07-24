package server

import (
	"net"

	"github.com/aagun1234/rabbit-mtcp-socks5/logger"
	"github.com/aagun1234/rabbit-mtcp-socks5/peer"
	"github.com/aagun1234/rabbit-mtcp-socks5/tunnel"

	//"crypto/cipher"

	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	// "github.com/gorilla/websocket"
)

type Server struct {
	peerGroup peer.PeerGroup
	logger    *logger.Logger
	keyfile   string
	certfile  string
	authkey   string
}

// GetPeerGroup 返回服务器的 PeerGroup
func (s *Server) GetPeerGroup() *peer.PeerGroup {
	return &s.peerGroup
}

func NewServer(cipher tunnel.Cipher, authkey, keyfile, certfile string) Server {
	return Server{
		peerGroup: peer.NewPeerGroup(cipher),
		logger:    logger.NewLogger("[Server]"),
		keyfile:   keyfile,
		certfile:  certfile,
		authkey:   authkey,
	}
}

func (s *Server) ServeThread(address string, wg *sync.WaitGroup) error {
	defer wg.Done()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	for {
		conn, err := listener.Accept()
		if err != nil {
			s.logger.Errorf("Error when accept connection: %v.\n", err)
			continue
		}
		err = s.peerGroup.AddTunnelFromConn(conn)
		if err != nil {
			s.logger.Errorf("Error when add tunnel to tunnel pool: %v.\n", err)
		}
	}
}

func (s *Server) Serve(addresses []string) error {
	var wg sync.WaitGroup

	for _, address := range addresses {
		wg.Add(1)
		s.logger.InfoAf("Listen on : %s.\n", address)
		go s.ServeThread(address, &wg)
	}
	wg.Wait()
	return nil
}

func getGoroutineID() int {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	idField := strings.Fields(strings.TrimPrefix(string(buf[:n]), "goroutine "))[0]
	id, err := strconv.Atoi(idField)
	if err != nil {
		panic(fmt.Sprintf("cannot get goroutine id: %v", err))
	}
	return id
}
