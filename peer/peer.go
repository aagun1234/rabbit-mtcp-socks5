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
	// 取消上下文，通知所有使用此上下文的协程退出
	p.cancel()
	
	// 关闭隧道池
	p.tunnelPool.Close()
	
	// 连接池会在上下文取消时自动关闭
	// 但为了确保安全，我们可以显式关闭它的通道
	// 注意：这里不需要额外的操作，因为连接池的recvRelay和sendRelay
	// 会在上下文取消时退出，并且removeConnection会在连接被移除时关闭通道
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
