package tunnel_pool

import (
	"context"
	"sync"

	"github.com/aagun1234/rabbit-mtcp-socks5/block"
	"github.com/aagun1234/rabbit-mtcp-socks5/logger"
	"github.com/aagun1234/rabbit-mtcp-socks5/stats"
)

type TunnelPool struct {
	mutex          sync.Mutex
	tunnelMapping  map[uint32]*Tunnel
	peerID         uint32
	manager        Manager
	sendQueue      chan block.Block
	sendRetryQueue chan block.Block
	recvQueue      chan block.Block
	ctx            context.Context
	cancel         context.CancelFunc // currently useless
	logger         *logger.Logger
}

func NewTunnelPool(peerID uint32, manager Manager, peerContext context.Context) *TunnelPool {
	ctx, cancel := context.WithCancel(peerContext)
	tp := &TunnelPool{
		tunnelMapping:  make(map[uint32]*Tunnel),
		peerID:         peerID,
		manager:        manager,
		sendQueue:      make(chan block.Block, SendQueueSize),
		sendRetryQueue: make(chan block.Block, SendQueueSize),
		recvQueue:      make(chan block.Block, RecvQueueSize),
		ctx:            ctx,
		cancel:         cancel,
		logger:         logger.NewLogger("[TunnelPool]"),
	}
	tp.logger.InfoAf("Tunnel Pool of peer %d created.\n", peerID)
	go manager.DecreaseNotify(tp)

	return tp
}

// Add a tunnel to tunnelPool and start bi-relay
func (tp *TunnelPool) AddTunnel(tunnel *Tunnel) {
	tp.logger.Debugf("Tunnel %d added to Peer %d.\n", tunnel.tunnelID, tp.peerID)
	tp.mutex.Lock()
	defer tp.mutex.Unlock()

	tp.tunnelMapping[tunnel.tunnelID] = tunnel
	tp.manager.Notify(tp)

	// 更新连接计数
	if tunnel.IsClientMode {
		stats.ClientStats.IncrementConnectionCount()
	} else {
		stats.ServerStats.IncrementConnectionCount()
	}

	tunnel.ctx, tunnel.cancel = context.WithCancel(tp.ctx)
	go func() {
		<-tunnel.ctx.Done()
		tp.RemoveTunnel(tunnel)
	}()

	go tunnel.OutboundRelay(tp.sendQueue, tp.sendRetryQueue)
	go tunnel.InboundRelay(tp.recvQueue)
	// 启动Ping-Pong协程
	go tunnel.PingPong()
}

// Remove a tunnel from tunnelPool and stop bi-relay
func (tp *TunnelPool) RemoveTunnel(tunnel *Tunnel) {
	tp.logger.Debugf("Tunnel %d to peer %d removed from pool.\n", tunnel.tunnelID, tunnel.peerID)
	tp.mutex.Lock()
	defer tp.mutex.Unlock()
	if tunnel, ok := tp.tunnelMapping[tunnel.tunnelID]; ok {
		delete(tp.tunnelMapping, tunnel.tunnelID)
		tp.manager.Notify(tp)
		go tp.manager.DecreaseNotify(tp)

		// 更新连接计数
		if tunnel.IsClientMode {
			stats.ClientStats.DecrementConnectionCount()
		} else {
			stats.ServerStats.DecrementConnectionCount()
		}
	}
}

func (tp *TunnelPool) GetSendQueue() chan block.Block {
	return tp.sendQueue
}

func (tp *TunnelPool) GetRecvQueue() chan block.Block {
	return tp.recvQueue
}

// GetConnectionsInfo 获取所有连接的详细信息
func (tp *TunnelPool) GetTunnelConnsInfo() []map[string]interface{} {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()

	result := make([]map[string]interface{}, 0, len(tp.tunnelMapping))
	tp.logger.Debugf("Length of tunnelMapping: %d.", len(tp.tunnelMapping))
	for _, tunn := range tp.tunnelMapping {
		tunnInfo := map[string]interface{}{
			"tunnel_id":     tunn.tunnelID,
			"peer_id":       tp.peerID,
			"last_activity": tunn.GetLastActiveStr(),
			"latency_nano":  tunn.GetLatencyNano(),
			"sent_bytes":    tunn.SentBytes,
			"recv_bytes":    tunn.RecvBytes,
			//"latency_nano":  fmt.Sprintf("%.2f us", tunn.GetLatencyNano()/1000),
			//"sent_bytes":    fmt.Sprintf("%.2f K", tunn.SentBytes/1024),
			//"recv_bytes":    fmt.Sprintf("%.2f K", tunn.RecvBytes/1024),
			"ws_raddr": tunn.Conn.RemoteAddr(),
			"ws_laddr": tunn.Conn.LocalAddr(),
		}
		tp.logger.Debugf("GetTunnelConnsInfo : TunnelID %d.", tunn.tunnelID)

		result = append(result, tunnInfo)
	}

	return result
}

func (tp *TunnelPool) GetTunnelPoolInfo() map[string]interface{} {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()
	tunnelPoolInfo := map[string]interface{}{
		"peer_id":                 tp.peerID,
		"recv_queue_length":       len(tp.recvQueue),
		"send_queue_length":       len(tp.sendQueue),
		"send_retry_queue_length": len(tp.sendRetryQueue),
		"tunnel_count":            len(tp.tunnelMapping),
	}
	return tunnelPoolInfo
}
