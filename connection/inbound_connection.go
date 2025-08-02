package connection

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net"
	"syscall"
	"time"

	"github.com/aagun1234/rabbit-mtcp-socks5/block"
	"github.com/aagun1234/rabbit-mtcp-socks5/logger"
	"go.uber.org/atomic"
)

type InboundConnection struct {
	baseConnection
	dataBuffer ByteRingBuffer

	writeCtx context.Context
	readCtx  context.Context

	readClosed  *atomic.Bool
	writeClosed *atomic.Bool

	LastActivity atomic.Int64
}

func NewInboundConnection(sendQueue chan<- block.Block, ctx context.Context, removeFromPool context.CancelFunc) Connection {
	connectionID := rand.Uint32()
	c := InboundConnection{
		baseConnection: baseConnection{
			blockProcessor:   newBlockProcessor(ctx, removeFromPool),
			connectionID:     connectionID,
			closed:           atomic.NewBool(false),
			sendQueue:        sendQueue,
			recvQueue:        make(chan block.Block, RecvQueueSize),
			orderedRecvQueue: make(chan block.Block, OrderedRecvQueueSize),
			logger:           logger.NewLogger(fmt.Sprintf("[InboundConnection-%d]", connectionID)),
		},
		dataBuffer:  NewByteRingBuffer(block.MaxSize),
		readCtx:     ctx,
		writeCtx:    ctx,
		readClosed:  atomic.NewBool(false),
		writeClosed: atomic.NewBool(false),
	}
	c.logger.InfoAf("InboundConnection %d created.\n", connectionID)
	c.SetLastActive()
	return &c
}

func (c *InboundConnection) SetLastActive() {
	c.LastActivity.Store(time.Now().UnixNano())
}

func (c *InboundConnection) GetLastActiveStr() string {
	return time.Unix(0, c.LastActivity.Load()).Format("2006-01-02 15:04:05.999999")
}

func (c *InboundConnection) GetLastActive() int64 {
	return c.LastActivity.Load()
}

func (c *InboundConnection) Read(b []byte) (n int, err error) {
	readN := 0

	if !c.dataBuffer.Empty() {
		// There's something left in buffer
		readN += c.dataBuffer.Read(b)
		if readN == len(b) {
			// if dst is full, return
			return readN, nil
		}
	}

	if c.closed.Load() || c.readClosed.Load() {
		// Connection is closed, should read all data left in channel
		for {
			select {
			case blk := <-c.orderedRecvQueue:
				_ = c.readBlock(&blk, &readN, b)
				if readN == len(b) {
					return readN, nil
				}
			default:
				if readN != 0 {
					return readN, nil
				} else {
					return 0, io.EOF
				}
			}
		}
	}

	// Read at lease something
	if readN == 0 {
		select {
		case blk := <-c.orderedRecvQueue:
			c.logger.Debugln("Read in a block.")
			err := c.readBlock(&blk, &readN, b)
			if err == io.EOF || readN == len(b) {
				if readN != 0 {
					return readN, nil
				} else {
					return 0, err
				}
			}
		case <-c.readCtx.Done():
			c.logger.InfoAln("ReadDeadline exceeded.")
			if readN != 0 {
				return readN, nil
			} else {
				return 0, io.EOF
			}
		}
	}

	if readN == 0 {
		c.logger.Errorln("Unknown error.")
	}

	for {
		select {
		case blk := <-c.orderedRecvQueue:
			err := c.readBlock(&blk, &readN, b)
			c.logger.Debugln("Read in a block.")
			if err == io.EOF || readN == len(b) {
				return readN, nil
			}
		case <-c.readCtx.Done():
			c.logger.InfoAln("ReadDeadline exceeded.")
			return readN, nil
		default:
			return readN, nil
		}
	}
}

func (c *InboundConnection) readBlock(blk *block.Block, readN *int, b []byte) (err error) {

	c.SetLastActive()

	switch blk.Type {
	case block.TypeDisconnect:
		// TODO: decide shutdown type
		switch blk.BlockData[0] {
		case block.ShutdownBoth:
			c.closed.Store(true)
			return io.EOF
		case block.ShutdownWrite:
			c.readClosed.Store(true)
			return io.EOF
		case block.ShutdownRead:
			c.writeClosed.Store(true)
			return nil
		}
	case block.TypeData:
		dst := b[*readN:]
		if len(dst) < len(blk.BlockData) {
			// if dst can't put a block, put part of it and return
			c.dataBuffer.OverWrite(blk.BlockData)
			readBytes := c.dataBuffer.Read(dst)
			*readN += readBytes
			// 更新接收字节计数
			c.RecvBytes.Add(uint64(readBytes))
			return
		}
		// if dst can put a block, put it
		copiedBytes := copy(dst, blk.BlockData)
		*readN += copiedBytes
		// 更新接收字节计数
		c.RecvBytes.Add(uint64(copiedBytes))
	case block.TypePing:
		c.logger.Debugf("InboundConnection received TypePing.\n")
	case block.TypePong:
		c.logger.Debugf("InboundConnection received TypePpng.\n")

	}
	return
}

func (c *InboundConnection) Write(b []byte) (n int, err error) {
	// TODO: tag all blocks from b using WaitGroup
	// TODO: and wait all blocks sent?
	if c.writeClosed.Load() || c.closed.Load() {
		return 0, syscall.EINVAL
	}
	c.sendData(b)
	// 更新发送字节计数
	c.SentBytes.Add(uint64(len(b)))
	return len(b), nil
}

func (c *InboundConnection) Close() error {
	if c.closed.CAS(false, true) {
		c.SendDisconnect(block.ShutdownBoth)
		close(c.recvQueue)        // 新增通道关闭
		close(c.orderedRecvQueue) // 新增通道关闭
	}
	c.Stop()
	return nil
}

func (c *InboundConnection) CloseRead() error {
	c.SendDisconnect(block.ShutdownRead)
	return nil
}

func (c *InboundConnection) CloseWrite() error {
	c.SendDisconnect(block.ShutdownWrite)
	return nil
}

func (c *InboundConnection) LocalAddr() net.Addr {
	// TODO
	return nil
}

func (c *InboundConnection) RemoteAddr() net.Addr {
	// TODO
	return nil
}

func (c *InboundConnection) SetDeadline(t time.Time) error {
	_ = c.SetReadDeadline(t)
	_ = c.SetWriteDeadline(t)
	return nil
}

func (c *InboundConnection) SetReadDeadline(t time.Time) error {
	c.readCtx, _ = context.WithDeadline(context.Background(), t)
	return nil
}

func (c *InboundConnection) SetWriteDeadline(t time.Time) error {
	c.writeCtx, _ = context.WithDeadline(context.Background(), t)
	return nil
}
