package peer

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"io"
	"math/rand"

	"github.com/aagun1234/rabbit-mtcp-socks5/connection_pool"
	"github.com/aagun1234/rabbit-mtcp-socks5/tunnel_pool"
)

type Peer struct {
	peerID         uint32
	connectionPool connection_pool.ConnectionPool
	tunnelPool     tunnel_pool.TunnelPool
	ctx            context.Context
	cancel         context.CancelFunc
}

func (p *Peer) Stop() {
	p.cancel()
}

// GetConnectionPool 返回连接池，供外部包访问
func (p *Peer) GetConnectionPool() *connection_pool.ConnectionPool {
	return &p.connectionPool
}

// GetTunnelPool 返回隧道池，供外部包访问
func (p *Peer) GetTunnelPool() *tunnel_pool.TunnelPool {
	return &p.tunnelPool
}

func initRand() error {
	seedSize := 8
	seedBytes := make([]byte, seedSize)
	_, err := io.ReadFull(crand.Reader, seedBytes)
	if err != nil {
		return err
	}
	rand.Seed(int64(binary.LittleEndian.Uint64(seedBytes)))
	return nil
}
