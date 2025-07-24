package connection

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/aagun1234/rabbit-mtcp-socks5/block"
	"github.com/aagun1234/rabbit-mtcp-socks5/logger"
	"go.uber.org/atomic"
)

type OutboundConnection struct {
	baseConnection
	HalfOpenConn
	ctx    context.Context
	cancel context.CancelFunc
}

func NewOutboundConnection(connectionID uint32, sendQueue chan<- block.Block, ctx context.Context, removeFromPool context.CancelFunc) Connection {
	c := OutboundConnection{
		baseConnection: baseConnection{
			blockProcessor:   newBlockProcessor(ctx, removeFromPool),
			connectionID:     connectionID,
			closed:           atomic.NewBool(true),
			sendQueue:        sendQueue,
			recvQueue:        make(chan block.Block, RecvQueueSize),
			orderedRecvQueue: make(chan block.Block, OrderedRecvQueueSize),
			logger:           logger.NewLogger(fmt.Sprintf("[OutboundConnection-%d]", connectionID)),
		},
		ctx:    ctx,
		cancel: removeFromPool,
	}
	c.logger.InfoAf("OutboundConnection %d created.\n", connectionID)
	return &c
}

func (oc *OutboundConnection) closeThenCancelWithOnceSend() {
	oc.HalfOpenConn.Close()
	oc.cancel()
	if oc.closed.CAS(false, true) {
		oc.SendDisconnect(block.ShutdownBoth)
	}
}

func (oc *OutboundConnection) closeThenCancel() {
	oc.HalfOpenConn.Close()
	oc.cancel()
}

// real connection -> ConnectionPool's SendQueue -> TunnelPool
func (oc *OutboundConnection) RecvRelay() {
	recvBuffer := make([]byte, OutboundRecvBuffer)
	for {
		oc.HalfOpenConn.SetReadDeadline(time.Now().Add(time.Duration(OutboundBlockTimeoutSec) * time.Second))
		n, err := oc.HalfOpenConn.Read(recvBuffer)
		if err == nil {
			oc.sendData(recvBuffer[:n])
			// 更新接收字节计数
			oc.RecvBytes.Add(uint64(n))
			oc.logger.InfoAf("OutboundConnection C->S %d bytes", n)
			oc.HalfOpenConn.SetReadDeadline(time.Time{})
		} else if err == io.EOF {
			oc.logger.Debugln("EOF received from outbound connection.")
			oc.closeThenCancelWithOnceSend()
			return
		} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			oc.logger.Debugln("Receive timeout from outbound connection.")
		} else {
			oc.logger.Errorf("Error when recv relay outbound connection: %v\n.", err)
			oc.closeThenCancelWithOnceSend()
			return
		}
		select {
		case <-oc.ctx.Done():
			// Should read all before leave, or packet will be lost
			for {
				n, err := oc.HalfOpenConn.Read(recvBuffer)
				if err == nil {
					oc.logger.Debugln("Data received from outbound connection successfully after close.")
					oc.sendData(recvBuffer[:n])
					// 更新接收字节计数
					oc.RecvBytes.Add(uint64(n))
				} else {
					oc.logger.Debugf("Error when receiving data from outbound connection after close: %v.\n", err)
					break
				}
			}
			return
		default:
			continue
		}
	}
}

// orderedRecvQueue -> real connection
func (oc *OutboundConnection) SendRelay() {
	for {
		select {
		case blk := <-oc.orderedRecvQueue:
			switch blk.Type {
			case block.TypeConnect:
				// Will do nothing!
				continue
			case block.TypePing:
				// Will do nothing!
				continue
			case block.TypePong:
				// Will do nothing!
				continue
			case block.TypeData:
				oc.logger.Debugln("Send out DATA bytes.")
				oc.HalfOpenConn.SetWriteDeadline(time.Now().Add(time.Duration(OutboundBlockTimeoutSec) * time.Second))
				n, err := oc.HalfOpenConn.Write(blk.BlockData)
				if err == nil {
					// 更新发送字节计数
					oc.SentBytes.Add(uint64(n))
					oc.HalfOpenConn.SetWriteDeadline(time.Time{})
				} else {
					oc.logger.Errorf("Error when send relay outbound connection: %v\n.", err)
					oc.closeThenCancelWithOnceSend()
				}
			case block.TypeDisconnect:
				if blk.BlockData[0] == block.ShutdownRead {
					oc.logger.Debugf("CloseRead for remote connection\n")
					oc.HalfOpenConn.CloseRead()
				} else if blk.BlockData[0] == block.ShutdownWrite {
					oc.logger.Debugf("CloseWrite for remote connection\n")
					oc.HalfOpenConn.CloseWrite()
				} else {
					oc.logger.Debugln("Send out DISCONNECT action.")
					oc.closeThenCancel()
				}
			}
		case <-oc.ctx.Done():
			oc.closeThenCancelWithOnceSend()
			return
		}
	}
}

func (oc *OutboundConnection) RecvBlock(blk block.Block) {
	if blk.Type == block.TypeConnect {
		address := string(blk.BlockData)
		oc.logger.Debugf("OutBoundConnection received TypeConnect for %s.\n", address)
		go oc.connect(address)
	}
	if blk.Type == block.TypePing {
		oc.logger.Debugf("OutBoundConnection received TypePing.\n")
	}
	if blk.Type == block.TypePong {
		oc.logger.Debugf("OutBoundConnection received TypePong.\n")
	}

	oc.recvQueue <- blk
}

func (oc *OutboundConnection) connect(address string) {
	oc.logger.Debugln("Send out CONNECTION action.")
	if !oc.closed.Load() || oc.HalfOpenConn != nil {
		return
	}
	dialTimeout := time.Duration(DialTimeoutSec) * time.Second
	rawConn, err := net.DialTimeout("tcp", address, dialTimeout)
	//rawConn, err := net.Dial("tcp", address)
	if err == nil {
		oc.logger.Infof("Dial to %s successfully.\n", address)
		oc.HalfOpenConn = rawConn.(*net.TCPConn)
		oc.closed.Toggle()
		go oc.RecvRelay()
		go oc.SendRelay()
	} else {
		oc.logger.Warnf("Error when dial to %s: %v.\n", address, err)
		oc.SendDisconnect(block.ShutdownBoth)
	}
}
