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
		// 首先检查上下文是否已取消
		select {
		case <-oc.ctx.Done():
			oc.logger.Debugln("RecvRelay exiting due to context cancellation")
			return
		default:
			// 继续执行
		}

		// 设置读取超时
		oc.HalfOpenConn.SetReadDeadline(time.Now().Add(time.Duration(OutboundBlockTimeoutSec) * time.Second))
		n, err := oc.HalfOpenConn.Read(recvBuffer)
		if err == nil {
			// 成功读取数据
			oc.sendData(recvBuffer[:n])
			// 更新接收字节计数
			oc.RecvBytes.Add(uint64(n))
			oc.logger.InfoAf("OutboundConnection C->S %d bytes", n)
			oc.HalfOpenConn.SetReadDeadline(time.Time{})
		} else if err == io.EOF {
			// 连接已关闭
			oc.logger.Debugln("EOF received from outbound connection.")
			oc.closeThenCancelWithOnceSend()
			return
		} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			// 读取超时，继续尝试
			oc.logger.Debugln("Receive timeout from outbound connection.")
		} else {
			// 其他错误
			oc.logger.Errorf("Error when recv relay outbound connection: %v\n.", err)
			oc.closeThenCancelWithOnceSend()
			return
		}

		// 检查上下文是否已取消
		select {
		case <-oc.ctx.Done():
			// 尝试读取剩余数据，但设置最大尝试次数和超时时间
			oc.logger.Debugln("Context cancelled, attempting to read remaining data")

			// 设置较短的读取超时
			oc.HalfOpenConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))

			// 最多尝试3次读取剩余数据
			for i := 0; i < 5; i++ {
				n, err := oc.HalfOpenConn.Read(recvBuffer)
				if err == nil && n > 0 {
					oc.logger.Debugf("Read %d remaining bytes after context cancellation", n)
					oc.sendData(recvBuffer[:n])
					oc.RecvBytes.Add(uint64(n))
				} else {
					// 出错或无数据可读，退出循环
					break
				}
			}

			oc.logger.Debugln("RecvRelay exiting after context cancellation")
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
		case <-oc.ctx.Done():
			// 上下文已取消，退出协程
			oc.logger.Debugln("SendRelay exiting due to context cancellation")
			return
		case blk, ok := <-oc.orderedRecvQueue:
			// 检查通道是否已关闭
			if !ok {
				oc.logger.Debugln("SendRelay exiting due to closed channel")
				return
			}

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

				// 检查连接是否有效
				if oc.HalfOpenConn == nil {
					oc.logger.Errorln("Cannot write to nil connection")
					oc.closeThenCancelWithOnceSend()
					continue
				}

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
				switch blk.BlockData[0] {
				case block.ShutdownRead:
					oc.logger.Debugf("CloseRead for remote connection\n")
					oc.HalfOpenConn.CloseRead()
				case block.ShutdownWrite:
					oc.logger.Debugf("CloseWrite for remote connection\n")
					oc.HalfOpenConn.CloseWrite()
				default:
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
