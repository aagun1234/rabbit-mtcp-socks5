package peer

import (
	"context"

	"github.com/aagun1234/rabbit-mtcp-socks5/connection_pool"
	"github.com/aagun1234/rabbit-mtcp-socks5/tunnel_pool"
)

type ServerPeer struct {
	Peer
}

func NewServerPeerWithID(peerID uint32, peerContext context.Context, removePeerFunc context.CancelFunc) ServerPeer {
	poolManager := tunnel_pool.NewServerManager(removePeerFunc)
	tunnelPool := tunnel_pool.NewTunnelPool(peerID, &poolManager, peerContext)

	connectionPool := connection_pool.NewConnectionPool(tunnelPool, true, peerContext)

	return ServerPeer{
		Peer: Peer{
			peerID:         peerID,
			connectionPool: *connectionPool,
			tunnelPool:     *tunnelPool,
			ctx:            peerContext,
			cancel:         removePeerFunc,
		},
	}
}
