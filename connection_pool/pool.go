package connection_pool

import (
	"context"
	"sync"

	"github.com/aagun1234/rabbit-mtcp-socks5/block"
	"github.com/aagun1234/rabbit-mtcp-socks5/connection"
	"github.com/aagun1234/rabbit-mtcp-socks5/logger"
	"github.com/aagun1234/rabbit-mtcp-socks5/stats"
	"github.com/aagun1234/rabbit-mtcp-socks5/tunnel_pool"
)

const (
	SendQueueSize = 64 // SendQueue channel cap
)

type ConnectionPool struct {
	connectionMapping   map[uint32]connection.Connection
	mappingLock         sync.RWMutex
	tunnelPool          *tunnel_pool.TunnelPool
	sendQueue           chan block.Block
	acceptNewConnection bool
	logger              *logger.Logger

	ctx    context.Context
	cancel context.CancelFunc
}

func NewConnectionPool(pool *tunnel_pool.TunnelPool, acceptNewConnection bool, backgroundCtx context.Context) *ConnectionPool {
	ctx, cancel := context.WithCancel(backgroundCtx)
	cp := &ConnectionPool{
		connectionMapping:   make(map[uint32]connection.Connection),
		tunnelPool:          pool,
		sendQueue:           make(chan block.Block, SendQueueSize),
		acceptNewConnection: acceptNewConnection,
		logger:              logger.NewLogger("[ConnectionPool]"),
		ctx:                 ctx,
		cancel:              cancel,
	}
	cp.logger.InfoAln("Connection Pool created.")
	go cp.sendRelay()
	go cp.recvRelay()
	return cp
}

// Create InboundConnection, and it to ConnectionPool and return
func (cp *ConnectionPool) NewPooledInboundConnection() connection.Connection {
	connCtx, removeConnFromPool := context.WithCancel(cp.ctx)
	c := connection.NewInboundConnection(cp.sendQueue, connCtx, removeConnFromPool)
	cp.addConnection(c)
	go func() {
		<-connCtx.Done()
		cp.removeConnection(c)
	}()
	return c
}

// Create OutboundConnection, and it to ConnectionPool and return
func (cp *ConnectionPool) NewPooledOutboundConnection(connectionID uint32) connection.Connection {
	connCtx, removeConnFromPool := context.WithCancel(cp.ctx)
	c := connection.NewOutboundConnection(connectionID, cp.sendQueue, connCtx, removeConnFromPool)
	cp.addConnection(c)
	go func() {
		<-connCtx.Done()
		cp.removeConnection(c)
	}()
	return c
}

func (cp *ConnectionPool) addConnection(conn connection.Connection) {
	cp.logger.InfoAf("Connection %d added to connection pool.\n", conn.GetConnectionID())
	cp.mappingLock.Lock()
	defer cp.mappingLock.Unlock()
	cp.connectionMapping[conn.GetConnectionID()] = conn

	stats.ClientStats.IncrementConnectionCount()
	stats.ServerStats.IncrementConnectionCount()

	go conn.OrderedRelay(conn)
}

func (cp *ConnectionPool) removeConnection(conn connection.Connection) {
	cp.logger.InfoAf("Connection %d removed from connection pool.\n", conn.GetConnectionID())
	cp.mappingLock.Lock()
	defer cp.mappingLock.Unlock()
	if _, ok := cp.connectionMapping[conn.GetConnectionID()]; ok {
		//conn.Close()

		delete(cp.connectionMapping, conn.GetConnectionID())
		stats.ClientStats.DecrementConnectionCount()
		stats.ServerStats.DecrementConnectionCount()

	}
}

// Deliver blocks from tunnelPool channel to specified connections
func (cp *ConnectionPool) recvRelay() {
	cp.logger.InfoAln("Recv Relay started.")
	for {
		select {
		case blk, ok := <-cp.tunnelPool.GetRecvQueue():
			// 检查通道是否已关闭
			if !ok {
				cp.logger.InfoAln("Recv Relay stopped due to closed channel.")
				return
			}
			
			connID := blk.ConnectionID
			var conn connection.Connection
			var connOk bool
			cp.mappingLock.RLock()
			conn, connOk = cp.connectionMapping[connID]
			cp.mappingLock.RUnlock()
			if !connOk {
				if cp.acceptNewConnection {
					conn = cp.NewPooledOutboundConnection(blk.ConnectionID)
					cp.logger.InfoAln("Connection created and added to connectionPool.")
				} else {
					cp.logger.Errorf("Unknown connection. blk.Type: %d, blk.Length: %d, ConnectionID: %d, ConnectionPool: %d", blk.Type, blk.BlockLength, connID, len(cp.connectionMapping))
					continue
				}
			}
			
			// 安全地发送数据块
			select {
			case <-cp.ctx.Done():
				cp.logger.InfoAln("Recv Relay stopped during block processing.")
				return
			default:
				conn.RecvBlock(blk)
				cp.logger.Debugf("Block %d(type: %d) put to connRecvQueue.\n", blk.BlockID, blk.Type)
			}
			
		case <-cp.ctx.Done():
			cp.logger.InfoAln("Recv Relay stopped.")
			return
		}
	}
}

// Deliver blocks from connPool's sendQueue to tunnelPool
// TODO: Maybe QOS can be implemented here
func (cp *ConnectionPool) sendRelay() {
	cp.logger.InfoAln("Send Relay started.")
	for {
		select {
		case blk, ok := <-cp.sendQueue:
			// 检查通道是否已关闭
			if !ok {
				cp.logger.InfoAln("Send Relay stopped due to closed channel.")
				return
			}
			
			// 安全地发送数据块到隧道池
			select {
			case <-cp.ctx.Done():
				cp.logger.InfoAln("Send Relay stopped during block processing.")
				return
			case cp.tunnelPool.GetSendQueue() <- blk:
				cp.logger.Debugf("Block %d(type: %d) put to connSendQueue.\n", blk.BlockID, blk.Type)
			}
			
		case <-cp.ctx.Done():
			cp.logger.InfoAln("Send Relay stopped.")
			return
		}
	}
}

func (cp *ConnectionPool) stopRelay() {
	cp.logger.Infoln("Stop all ConnectionPool Relay.")
	cp.cancel()
}

// GetConnectionsInfo 获取所有连接的详细信息
func (cp *ConnectionPool) GetConnectionsInfo() []map[string]interface{} {
	cp.mappingLock.RLock()
	defer cp.mappingLock.RUnlock()

	result := make([]map[string]interface{}, 0, len(cp.connectionMapping))
	cp.logger.Debugf("Length of connectionMapping: %d.", len(cp.connectionMapping))
	for _, conn := range cp.connectionMapping {
		connInfo := map[string]interface{}{
			"connection_id":             conn.GetConnectionID(),
			"last_activity":             conn.GetLastActiveStr(),
			"latency_nano":              conn.GetLatencyNano(),
			"recv_queue_length":         len(conn.GetRecvQueue()),
			"ordered_recv_queue_length": len(conn.GetOrderedRecvQueue()),
			"sent_bytes":                conn.GetSentBytes(),
			"recv_bytes":                conn.GetRecvBytes(),
		}
		cp.logger.Debugf("ConnecttionID: %s.", conn.GetConnectionID())

		result = append(result, connInfo)
	}

	return result
}

func (cp *ConnectionPool) GetConnectionPoolInfo() map[string]interface{} {
	cp.mappingLock.RLock()
	defer cp.mappingLock.RUnlock()

	connectionPoolInfo := map[string]interface{}{
		"connection_count":      len(cp.connectionMapping),
		"send_queue_length":     len(cp.sendQueue),
		"tunnel_pool_size":      cp.tunnelPool.GetTunnelMappingLen(),
		"accept_new_connection": cp.acceptNewConnection,
	}
	return connectionPoolInfo
}

// GetConnectionsInfo 获取所有连接的详细信息
func (cp *ConnectionPool) GetTunnelsInfo() []map[string]interface{} {
	cp.mappingLock.RLock()
	defer cp.mappingLock.RUnlock()

	result := cp.tunnelPool.GetTunnelConnsInfo()
	return result
}

func (cp *ConnectionPool) GetTunnelPoolInfo() map[string]interface{} {
	cp.mappingLock.RLock()
	defer cp.mappingLock.RUnlock()

	return cp.tunnelPool.GetTunnelPoolInfo()
}
