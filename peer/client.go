package peer

import (
	"context"
	"math/rand"

	"github.com/aagun1234/rabbit-mtcp-socks5/connection"
	"github.com/aagun1234/rabbit-mtcp-socks5/connection_pool"
	"github.com/aagun1234/rabbit-mtcp-socks5/tunnel"
	"github.com/aagun1234/rabbit-mtcp-socks5/tunnel_pool"
)

type ClientPeer struct {
	Peer
}

func NewClientPeer(tunnelNum int, endpoints []string, cipher tunnel.Cipher, authkey string, insecure bool, retryfailed bool) ClientPeer {
	if initRand() != nil {
		panic("Error when initialize random seed.")
	}
	peerID := rand.Uint32()
	return newClientPeerWithID(peerID, tunnelNum, endpoints, cipher, authkey, insecure, retryfailed)
}

func newClientPeerWithID(peerID uint32, tunnelNum int, endpoints []string, cipher tunnel.Cipher, authkey string, insecure bool, retryfailed bool) ClientPeer {
	peerCtx, removePeerFunc := context.WithCancel(context.Background())

	poolManager := tunnel_pool.NewClientManager(tunnelNum, endpoints, peerID, cipher, authkey, insecure, retryfailed)
	tunnelPool := tunnel_pool.NewTunnelPool(peerID, &poolManager, peerCtx)
	connectionPool := connection_pool.NewConnectionPool(tunnelPool, false, peerCtx)

	return ClientPeer{
		Peer: Peer{
			peerID:         peerID,
			connectionPool: *connectionPool,
			tunnelPool:     *tunnelPool,
			ctx:            peerCtx,
			cancel:         removePeerFunc,
		},
	}
}

func (cp *ClientPeer) Dial(address string) connection.Connection {
	conn := cp.connectionPool.NewPooledInboundConnection()
	conn.SendConnect(address)
	return conn
}
