package connection

import (
	"net"
	"time"

	"github.com/aagun1234/rabbit-mtcp-socks5/block"
	"github.com/aagun1234/rabbit-mtcp-socks5/logger"

	//	"github.com/gorilla/websocket"
	"go.uber.org/atomic"
)

type HalfOpenConn interface {
	net.Conn
	CloseRead() error
	CloseWrite() error
}

type CloseWrite interface {
	CloseWrite() error
}

type CloseRead interface {
	CloseRead() error
}

type Connection interface {
	HalfOpenConn
	GetConnectionID() uint32
	GetOrderedRecvQueue() chan block.Block
	GetRecvQueue() chan block.Block
	GetSentBytes() uint64
	GetRecvBytes() uint64
	GetLastActiveStr() string
	GetLatencyNano() int64

	RecvBlock(block.Block)

	SendConnect(address string)
	SendDisconnect(uint8)

	OrderedRelay(connection Connection) // Run orderedRelay infinitely
	Stop()                              // Stop all related relay and remove itself from connectionPool
}

type baseConnection struct {
	blockProcessor   blockProcessor
	connectionID     uint32
	closed           *atomic.Bool
	sendQueue        chan<- block.Block // Same as connectionPool
	recvQueue        chan block.Block
	orderedRecvQueue chan block.Block
	logger           *logger.Logger
	LastActivity     atomic.Int64
	LatencyNano      atomic.Int64
	SentBytes        atomic.Uint64 // 发送字节计数
	RecvBytes        atomic.Uint64 // 接收字节计数
}

func (bc *baseConnection) SetLastActive() {
	bc.LastActivity.Store(time.Now().UnixNano())
}

func (bc *baseConnection) GetLastActiveStr() string {
	return time.Unix(0, bc.LastActivity.Load()).Format("2006-01-02 15:04:05.999999")
}

func (bc *baseConnection) GetLastActive() int64 {
	return bc.LastActivity.Load()
}

func (bc *baseConnection) SetLatencyNanoSince(timestamp int64) {
	bc.LatencyNano.Store(time.Now().UnixNano() - timestamp)
}
func (bc *baseConnection) GetLatencyNano() int64 {
	return bc.LatencyNano.Load()
}

func (bc *baseConnection) Stop() {
	bc.logger.Debugf("connection stop\n")
	bc.blockProcessor.removeFromPool()
}

func (bc *baseConnection) OrderedRelay(connection Connection) {
	bc.blockProcessor.OrderedRelay(connection)
}

func (bc *baseConnection) GetConnectionID() uint32 {
	return bc.connectionID
}

func (bc *baseConnection) GetRecvQueue() chan block.Block {
	return bc.recvQueue
}

func (bc *baseConnection) GetOrderedRecvQueue() chan block.Block {
	return bc.orderedRecvQueue
}

// GetSentBytes 获取发送字节数
func (bc *baseConnection) GetSentBytes() uint64 {
	return bc.SentBytes.Load()
}

// GetRecvBytes 获取接收字节数
func (bc *baseConnection) GetRecvBytes() uint64 {
	return bc.RecvBytes.Load()
}

func (bc *baseConnection) RecvBlock(blk block.Block) {
	bc.recvQueue <- blk
}

func (bc *baseConnection) SendConnect(address string) {
	bc.logger.Debugf("Send connect to %s block.\n", address)
	blk := bc.blockProcessor.packConnect(address, bc.connectionID)
	bc.sendQueue <- blk
}

func (bc *baseConnection) SendDisconnect(shutdownType uint8) {
	bc.logger.Debugf("Send disconnect block: %v\n", shutdownType)
	blk := bc.blockProcessor.packDisconnect(bc.connectionID, shutdownType)
	bc.sendQueue <- blk
	if shutdownType == block.ShutdownBoth {
		bc.Stop()
	}
}

func (bc *baseConnection) sendData(data []byte) {
	bc.logger.Debugln("Send data block.")
	blocks := bc.blockProcessor.packData(data, bc.connectionID)
	for _, blk := range blocks {
		bc.sendQueue <- blk
	}
}
