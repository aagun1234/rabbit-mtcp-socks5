package tunnel_pool

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"time"

	"github.com/aagun1234/rabbit-mtcp-socks5/block"
	"github.com/aagun1234/rabbit-mtcp-socks5/logger"
	"github.com/aagun1234/rabbit-mtcp-socks5/stats"
	"github.com/aagun1234/rabbit-mtcp-socks5/tunnel"
	"go.uber.org/atomic"
)

type Tunnel struct {
	net.Conn
	//websocket.Conn
	ctx          context.Context
	cancel       context.CancelFunc
	tunnelID     uint32
	peerID       uint32
	logger       *logger.Logger
	RecvBytes    uint64
	SentBytes    uint64
	LastActivity atomic.Int64
	LatencyNano  atomic.Int64
	IsClientMode bool // 标识是客户端还是服务端模式
	IsActive     bool
}

// Create a new tunnel from a net.Conn and cipher with random tunnelID
// func NewActiveTunnel(conn net.Conn, ciph tunnel.Cipher, peerID uint32) (Tunnel, error) {
func NewActiveTunnel(wsConn net.Conn, ciph tunnel.Cipher, peerID uint32) (Tunnel, error) {
	tun := newTunnelWithID(wsConn, ciph, peerID)
	tun.IsClientMode = true // 客户端主动创建隧道
	return tun, tun.activeExchangePeerID()
}

// func NewPassiveTunnel(conn net.Conn, ciph tunnel.Cipher) (Tunnel, error) {
func NewPassiveTunnel(wsConn net.Conn, ciph tunnel.Cipher) (Tunnel, error) {
	tun := newTunnelWithID(wsConn, ciph, 0)
	tun.IsClientMode = false // 服务端被动接受隧道
	return tun, tun.passiveExchangePeerID()
}

// Create a new tunnel from a net.Conn and cipher with given tunnelID
// func newTunnelWithID(conn net.Conn, ciph tunnel.Cipher, peerID uint32) Tunnel {
func newTunnelWithID(wsConn net.Conn, ciph tunnel.Cipher, peerID uint32) Tunnel {
	tunnelID := rand.Uint32()
	tun := Tunnel{
		Conn: tunnel.NewEncryptedConn(wsConn, ciph),
		//Conn:     &WebsocketConnAdapter{Conn: wsConn, writeMu: sync.Mutex{}},
		peerID:   peerID,
		tunnelID: tunnelID,
		logger:   logger.NewLogger(fmt.Sprintf("[Tunnel-%d]", tunnelID)),
	}
	tun.logger.InfoAf("Tunnel %d created.", tunnelID)
	return tun
}

func (tunnel *Tunnel) SetLastActive() {
	tunnel.LastActivity.Store(time.Now().UnixNano())
	tunnel.IsActive = true
}

func (tunnel *Tunnel) GetLastActiveStr() string {
	return time.Unix(0, tunnel.LastActivity.Load()).Format("2006-01-02 15:04:05.999999")
}

func (tunnel *Tunnel) GetLastActive() int64 {
	return tunnel.LastActivity.Load()
}

func (tunnel *Tunnel) SetLatencyNanoSince(timestamp int64) {
	tunnel.LatencyNano.Store(time.Now().UnixNano() - timestamp)
}
func (tunnel *Tunnel) SetLatencyNano(latency int64) {
	tunnel.LatencyNano.Store(latency)
}
func (tunnel *Tunnel) GetLatencyNano() int64 {
	return tunnel.LatencyNano.Load()
}

func (tunnel *Tunnel) activeExchangePeerID() (err error) {
	err = tunnel.sendPeerID(tunnel.peerID)
	if err != nil {
		tunnel.logger.Errorf("Cannot exchange peerID(send failed: %v).\n", err)
		return err
	}
	peerID, err := tunnel.recvPeerID()
	if err != nil {
		tunnel.logger.Errorf("Cannot exchange peerID(recv failed: %v).\n", err)
		return err
	}
	if tunnel.peerID != peerID {
		tunnel.logger.Errorf("Cannot exchange peerID(local: %d, remote: %d).\n", tunnel.peerID, peerID)
		return errors.New("invalid exchanging")
	}
	tunnel.logger.Infof("PeerID %d exchange successfully.", peerID)
	return
}

func (tunnel *Tunnel) passiveExchangePeerID() (err error) {
	peerID, err := tunnel.recvPeerID()
	if err != nil {
		tunnel.logger.Errorf("Cannot exchange peerID(recv failed: %v).\n", err)
		return err
	}
	err = tunnel.sendPeerID(peerID)
	if err != nil {
		tunnel.logger.Errorf("Cannot exchange peerID(send failed: %v).\n", err)
		return err
	}
	tunnel.peerID = peerID
	tunnel.logger.Infof("PeerID %d exchange successfully.", peerID)
	return
}

func (tunnel *Tunnel) sendPeerID(peerID uint32) error {
	peerIDBuffer := make([]byte, 4)
	binary.LittleEndian.PutUint32(peerIDBuffer, peerID)
	_, err := io.CopyN(tunnel.Conn, bytes.NewReader(peerIDBuffer), 4)
	if err != nil {
		tunnel.logger.Errorf("Peer id sent with error:%v.\n", err)
		return err
	}
	tunnel.logger.Debugln("Peer id sent.")
	return nil
}

func (tunnel *Tunnel) recvPeerID() (uint32, error) {
	peerIDBuffer := make([]byte, 4)
	tunnel.logger.Debugf("recvPeerID Set tunnel.Conn ReadDeadline 30S.\n")
	deadline := time.Now().Add(30 * time.Second)
	tunnel.Conn.SetReadDeadline(deadline)
	_, err := io.ReadFull(tunnel.Conn, peerIDBuffer)
	tunnel.logger.Debugf("recvPeerID Set tunnel.Conn ReadDeadline unlimit.\n")
	tunnel.Conn.SetReadDeadline(time.Time{})
	if err != nil {
		tunnel.logger.Errorf("Peer id recv with error:%v.\n", err)
		return 0, err
	}
	peerID := binary.LittleEndian.Uint32(peerIDBuffer)
	tunnel.logger.Debugln("Peer id recv.")
	return peerID, nil
}

// Read block from send channel, pack it and send, client to server
func (tunnel *Tunnel) OutboundRelay(normalQueue, retryQueue chan block.Block) {
	// 确保 ctx 已经初始化
	if tunnel.ctx == nil {
		tunnel.logger.Errorln("OutboundRelay aborted: context not initialized")
		return
	}

	tunnel.logger.InfoAf("Outbound relay started. (PeerID: %d)", tunnel.peerID)
	for {
		// cancel is of highest priority
		select {
		case <-tunnel.ctx.Done():
			tunnel.logger.InfoAf("Outbound relay(cancel) ended. (PeerID: %d)", tunnel.peerID)
			return
		default:
		}
		// retryQueue is of secondary highest priority
		select {
		case <-tunnel.ctx.Done():
			tunnel.logger.InfoAf("Outbound relay(retry) ended. (PeerID: %d)", tunnel.peerID)
			return
		case blk := <-retryQueue:
			tunnel.packThenSend(blk, retryQueue)
		default:
		}
		// normalQueue is of secondary highest priority
		select {
		case <-tunnel.ctx.Done():
			tunnel.logger.InfoAf("Outbound relay(normal) ended. (PeerID: %d)", tunnel.peerID)
			return
		case blk := <-retryQueue:
			tunnel.packThenSend(blk, retryQueue)
		case blk := <-normalQueue:
			tunnel.packThenSend(blk, retryQueue)
		}
	}
}

func (tunnel *Tunnel) packThenSend(blk block.Block, retryQueue chan block.Block) {
	dataToSend := blk.Pack()
	reader := bytes.NewReader(dataToSend)

	tunnel.Conn.SetWriteDeadline(time.Now().Add(time.Duration(TunnelBlockTimeoutSec) * time.Second))
	n, err := io.Copy(tunnel.Conn, reader)

	if err == nil && n == int64(len(dataToSend)) {
		// 更新发送字节统计
		tunnel.SentBytes += uint64(n)

		// 根据模式更新全局统计
		if tunnel.IsClientMode {
			stats.ClientStats.AddSentBytes(uint64(n))
		} else {
			stats.ServerStats.AddSentBytes(uint64(n))
		}
		
		// 成功发送，重置写入超时
		tunnel.Conn.SetWriteDeadline(time.Time{})
		tunnel.logger.Debugf("Copied data to tunnel successfully(n: %d).\n", n)
		return
	}

	// 发送失败处理
	tunnel.logger.Warnf("Error when send bytes to tunnel: (n: %d, error: %v).\n", n, err)
	
	// 关闭隧道
	tunnel.closeThenCancel()
	
	// 只有数据块才需要重试
	if blk.Type == block.TypeData && retryQueue != nil {
		// 直接在当前协程中尝试发送，使用非阻塞方式
		select {
		case retryQueue <- blk:
			// 发送成功
			tunnel.logger.Debugf("Block %d put to retry queue successfully.\n", blk.BlockID)
		case <-time.After(3 * time.Second):
			// 发送超时
			tunnel.logger.Warnf("Timeout when putting block %d to retry queue, data may be lost.\n", blk.BlockID)
		case <-tunnel.ctx.Done():
			// 上下文已取消，不再重试
			tunnel.logger.Warnf("Context canceled when putting block %d to retry queue, data may be lost.\n", blk.BlockID)
		}
	}
}

// Read bytes from connection, parse it to block then put in recv channel
func (tunnel *Tunnel) InboundRelay(output chan<- block.Block) {
	// 确保 ctx 已经初始化
	if tunnel.ctx == nil {
		tunnel.logger.Errorln("InboundRelay aborted: context not initialized")
		return
	}

	tunnel.logger.InfoAf("Inbound relay started. (PeerID: %d)", tunnel.peerID)
	for {
		select {
		case <-tunnel.ctx.Done():
			// Should read all before leave, or packet will be lost
			tunnel.logger.Debugln("Context done, cleaning up tunnel connection.")
			// 设置较短的读取超时，避免长时间阻塞
			tunnel.Conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			for i := 0; i < 5; i++ { // 最多尝试读取5次，避免无限循环
				// Will never be blocked because the tunnel is closed
				blk, err := block.NewBlockFromReader(tunnel.Conn)
				if err == nil {
					tunnel.logger.Debugf("Block received from tunnel(type: %d) successfully after close.\n", blk.Type)

					// 更新接收字节统计
					receivedBytes := uint64(len(blk.Pack()))
					tunnel.RecvBytes += receivedBytes

					// 根据模式更新全局统计
					if tunnel.IsClientMode {
						stats.ClientStats.AddRecvBytes(receivedBytes)
					} else {
						stats.ServerStats.AddRecvBytes(receivedBytes)
					}

					// 使用非阻塞方式发送，避免在关闭时阻塞
					select {
					case output <- *blk:
						// 发送成功
					case <-time.After(500 * time.Millisecond):
						// 发送超时，记录日志
						tunnel.logger.Warnf("Timeout when sending block to output channel after close.\n")
						break
					}

				} else {
					tunnel.logger.Debugf("No more data or error when receiving block from tunnel after close: %v.\n", err)
					break
				}
			}
			tunnel.logger.InfoAf("Inbound relay ended. (PeerID: %d)", tunnel.peerID)
			return
		default:
			blk, err := block.NewBlockFromReader(tunnel.Conn)
			if err != nil {
				// Server will never close connection in normal cases
				tunnel.logger.Errorf("Error when receiving block from tunnel: %v.\n", err)
				// Tunnel down and message has not been fully read.
				tunnel.closeThenCancel()
			} else {
				tunnel.logger.Debugf("Block received from tunnel(type: %d)successfully.\n", blk.Type)
				tunnel.SetLastActive()

				// 更新接收字节统计
				receivedBytes := uint64(len(blk.Pack()))
				tunnel.RecvBytes += receivedBytes

				// 根据模式更新全局统计
				if tunnel.IsClientMode {
					stats.ClientStats.AddRecvBytes(receivedBytes)
				} else {
					stats.ServerStats.AddRecvBytes(receivedBytes)
				}

				if blk.Type == block.TypePing {
					clatency := int64(binary.LittleEndian.Uint64(blk.BlockData))
					tunnel.SetLatencyNano(clatency)
					tunnel.logger.Infof("Ping-Pong client latency: %d us\n", clatency/1000)

					pongblk := block.NewPongBlock(0, 0, uint64(blk.TimeStamp))
					tunnel.logger.Debugf("Sending Pong to tunnel, with payload timestamp: %s", time.Unix(0, blk.TimeStamp).Format("2006-01-02 15:04:05.999999"))
					tunnel.packThenSend(pongblk, nil)

				} else if blk.Type == block.TypePong {
					tunnel.logger.Debugf("InboundRelay received TypePong.\n")
					timestamp := int64(binary.LittleEndian.Uint64(blk.BlockData))
					tunnel.SetLatencyNanoSince(timestamp)
					tunnel.logger.Infof("Ping-Pong Latency: %d us", tunnel.GetLatencyNano()/1000)
				} else {

					output <- *blk
				}

			}
		}
	}
}

// Read bytes from connection, parse it to block then put in recv channel
func (tunnel *Tunnel) PingPong() {
	// 确保 ctx 已经初始化
	if tunnel.ctx == nil {
		tunnel.logger.Errorln("PingPong aborted: context not initialized")
		return
	}

	tunnel.logger.InfoAln("PingPong started.")
	tunnel.SetLastActive()
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-tunnel.ctx.Done():
			tunnel.logger.InfoAln("PingPong ended.")
			return
		case <-ticker.C:

			//tunnel.logger.Debugf("%d,%d",(nowstamp-tunnel.LastActivity.Load())/1e9,PingInterval)
			if (time.Now().UnixNano()-tunnel.GetLastActive())/1e9 > int64(PingInterval) {
				blk := block.NewPingBlock(tunnel.tunnelID, 0, uint64(tunnel.GetLatencyNano()))
				tunnel.logger.Debugf("Sending Ping to tunnel, with local latency: %d us", tunnel.GetLatencyNano()/1000)
				tunnel.packThenSend(blk, nil)
			}
			if (time.Now().UnixNano()-tunnel.GetLastActive())/1e9 > int64(PingInterval+TunnelRecvTimeoutSec) {
				tunnel.logger.Warnln("PingPong timeout, going to close tunnel.")
				tunnel.IsActive = false
				tunnel.closeThenCancel()
				break
			}
		}
	}

}

func (tunnel *Tunnel) GetPeerID() uint32 {
	return tunnel.peerID
}

func (tunnel *Tunnel) closeThenCancel() {
	tunnel.Close()
	tunnel.cancel()
}
