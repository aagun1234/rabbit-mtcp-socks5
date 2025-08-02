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
		stats.ClientStats.IncrementTunnelCount()
	} else {
		stats.ServerStats.IncrementTunnelCount()
	}

	go func() {
		<-tunnel.ctx.Done()
		tp.RemoveTunnel(tunnel)
	}()

	go tunnel.OutboundRelay(tp.sendQueue, tp.sendRetryQueue)
	go tunnel.InboundRelay(tp.recvQueue)
	// 启动Ping-Pong协程
	if PingInterval > 0 {
		go tunnel.PingPong()
	} else {
		tp.logger.Warnln("Do not ping-pong.\n")
	}
}

// Remove a tunnel from tunnelPool and stop bi-relay
func (tp *TunnelPool) RemoveTunnel(tunnel *Tunnel) {
	tp.logger.Debugf("Tunnel %d to peer %d removed from pool.\n", tunnel.tunnelID, tunnel.peerID)
	tp.mutex.Lock()
	defer tp.mutex.Unlock()
	if tunnel1, ok := tp.tunnelMapping[tunnel.tunnelID]; ok {
		// 首先从映射中删除隧道，防止其他协程访问
		delete(tp.tunnelMapping, tunnel1.tunnelID)
		
		// 确保隧道已关闭（通过调用closeThenCancel）
		// 注意：tunnel.ctx.Done()会在closeThenCancel被调用时触发
		// 所以这里不需要再次调用Close
		
		// 通知管理器隧道已移除
		tp.manager.Notify(tp)
		go tp.manager.DecreaseNotify(tp)

		// 更新连接计数
		if tunnel1.IsClientMode {
			stats.ClientStats.DecrementTunnelCount()
		} else {
			stats.ServerStats.DecrementTunnelCount()
		}
	}
}

func (tp *TunnelPool) GetSendQueue() chan block.Block {
	return tp.sendQueue
}

func (tp *TunnelPool) GetRecvQueue() chan block.Block {
	return tp.recvQueue
}

func (tp *TunnelPool) GetTunnelMappingLen() int {
	return len(tp.tunnelMapping)
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
			"is_active":     tunn.IsActive,
			"last_activity": tunn.GetLastActiveStr(),
			"latency_nano":  tunn.GetLatencyNano(),
			"sent_bytes":    tunn.SentBytes,
			"recv_bytes":    tunn.RecvBytes,
			//"latency_nano":  fmt.Sprintf("%.2f us", tunn.GetLatencyNano()/1000),
			//"sent_bytes":    fmt.Sprintf("%.2f K", tunn.SentBytes/1024),
			//"recv_bytes":    fmt.Sprintf("%.2f K", tunn.RecvBytes/1024),
			"ws_remote_addr": tunn.Conn.RemoteAddr(),
			"ws_local_addr":  tunn.Conn.LocalAddr(),
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

// Close 安全地关闭隧道池及其所有资源
func (tp *TunnelPool) Close() {
	tp.logger.InfoAf("Closing tunnel pool for peer %d\n", tp.peerID)
	
	// 取消上下文，通知所有使用此上下文的协程退出
	tp.cancel()
	
	// 关闭所有隧道
	tp.mutex.Lock()
	tunnels := make([]*Tunnel, 0, len(tp.tunnelMapping))
	for _, tunnel := range tp.tunnelMapping {
		tunnels = append(tunnels, tunnel)
	}
	tp.mutex.Unlock()
	
	// 在锁外关闭隧道，避免死锁
	for _, tunnel := range tunnels {
		tunnel.closeThenCancel()
	}
	
	// 等待所有隧道关闭
	// 注意：这里不使用等待，因为RemoveTunnel会在隧道关闭时被调用
	// 而是确保所有通道都被安全关闭
	
	// 安全地关闭通道
	// 注意：先关闭发送通道，再关闭接收通道
	close(tp.sendQueue)
	close(tp.sendRetryQueue)
	close(tp.recvQueue)
	
	tp.logger.InfoAf("Tunnel pool for peer %d closed\n", tp.peerID)
}
