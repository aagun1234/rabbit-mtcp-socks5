package peer

import (
	"context"
	"net"

	"github.com/aagun1234/rabbit-mtcp-socks5/connection_pool"
	"github.com/aagun1234/rabbit-mtcp-socks5/logger"
	"github.com/aagun1234/rabbit-mtcp-socks5/tunnel"
	"github.com/aagun1234/rabbit-mtcp-socks5/tunnel_pool"

	//"github.com/gorilla/websocket"

	//"net"

	"sync"
	// "github.com/gorilla/websocket"
)

type PeerGroup struct {
	lock        sync.Mutex
	cipher      tunnel.Cipher
	peerMapping map[uint32]*ServerPeer
	logger      *logger.Logger
}

func NewPeerGroup(cipher tunnel.Cipher) PeerGroup {
	if initRand() != nil {
		panic("Error when initialize random seed.")
	}
	return PeerGroup{
		cipher:      cipher,
		peerMapping: make(map[uint32]*ServerPeer),
		logger:      logger.NewLogger("[PeerGroup]"),
	}
}

// Add a tunnel to it's peer; will create peer if not exists
func (pg *PeerGroup) AddTunnel(tunnel *tunnel_pool.Tunnel) error {
	// add tunnel to peer(if absent, create peer to peer_group)
	pg.lock.Lock()
	var peer *ServerPeer
	var ok bool

	peerID := tunnel.GetPeerID()
	if peer, ok = pg.peerMapping[peerID]; !ok {
		peerContext, removePeerFunc := context.WithCancel(context.Background())
		serverPeer := NewServerPeerWithID(peerID, peerContext, removePeerFunc)
		peer = &serverPeer
		pg.peerMapping[peerID] = peer
		pg.logger.InfoAf("Server Peer %d added to PeerGroup.\n", peerID)

		go func() {
			<-peerContext.Done()
			pg.RemovePeer(peerID)
		}()
	}
	pg.lock.Unlock()
	peer.tunnelPool.AddTunnel(tunnel)

	return nil
}

// Like AddTunnel, add a raw connection
func (pg *PeerGroup) AddTunnelFromConn(conn net.Conn) error {
	tun, err := tunnel_pool.NewPassiveTunnel(conn, pg.cipher)
	if err != nil {
		conn.Close()
		return err
	}

	return pg.AddTunnel(&tun)
}

func (pg *PeerGroup) RemovePeer(peerID uint32) {
	pg.logger.InfoAf("Server Peer %d removed from peer group.\n", peerID)
	pg.lock.Lock()
	defer pg.lock.Unlock()
	delete(pg.peerMapping, peerID)
}

// GetAllConnectionPools 返回所有 ServerPeer 的连接池
func (pg *PeerGroup) GetAllConnectionPools() []*connection_pool.ConnectionPool {
	pg.lock.Lock()
	defer pg.lock.Unlock()

	pools := make([]*connection_pool.ConnectionPool, 0, len(pg.peerMapping))
	pg.logger.Debugf("Length of pools: %d.\n", len(pg.peerMapping))
	for _, peer := range pg.peerMapping {
		pg.logger.Debugf("Adding ConnectionPool of Peer: %d.\n", peer.peerID)
		pools = append(pools, peer.GetConnectionPool())
	}

	return pools
}

// GetAllConnectionPools 返回所有 ServerPeer 的连接池
func (pg *PeerGroup) GetAllTunnelPools() []*tunnel_pool.TunnelPool {
	pg.lock.Lock()
	defer pg.lock.Unlock()

	pools := make([]*tunnel_pool.TunnelPool, 0, len(pg.peerMapping))
	pg.logger.Debugf("Length of Tunnelpools: %d.\n", len(pg.peerMapping))
	for _, peer := range pg.peerMapping {
		pg.logger.Debugf("Adding TunnelPool of Peer: %d.\n", peer.peerID)
		pools = append(pools, peer.GetTunnelPool())
	}

	return pools
}
