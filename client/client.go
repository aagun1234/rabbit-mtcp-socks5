package client

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aagun1234/rabbit-mtcp-socks5/connection"
	"github.com/aagun1234/rabbit-mtcp-socks5/logger"
	"github.com/aagun1234/rabbit-mtcp-socks5/peer"
	"github.com/aagun1234/rabbit-mtcp-socks5/tunnel"
)

type Client struct {
	Peer    peer.ClientPeer
	logger  *logger.Logger
	authkey string
}

func NewClient(tunnelNum int, endpoints []string, cipher tunnel.Cipher, authkey string, insecure bool, retryfailed bool) Client {
	return Client{
		Peer:    peer.NewClientPeer(tunnelNum, endpoints, cipher, authkey, insecure, retryfailed),
		logger:  logger.NewLogger("[Client]"),
		authkey: authkey,
	}
}

func (c *Client) Dial(address string) connection.HalfOpenConn {
	return c.Peer.Dial(address)
}

func (c *Client) Cancel() {
	c.Peer.Stop()
}

func (c *Client) ServeForward(listen, dest string) error {
	c.logger.Infof("Listen on %s for target %s \n", listen, dest)
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			c.logger.Errorf("Error when accept connection: %v.\n", err)
			continue
		}
		go func() {
			c.logger.Infof("Accepted a connection from %s for target %s \n", conn.RemoteAddr(), dest)
			//c.logger.Infoln("Accepted a connection.")
			connProxy := c.Dial(dest) //调用peer.Dial，由对端节点处理
			biRelay(conn.(*net.TCPConn), connProxy, c.logger)
		}()
	}
}

// 解析socks5://格式的地址，返回监听地址、用户名和密码
func parseSocks5URL(addr string) (listenAddr, username, password string, requireAuth bool, err error) {
	// 检查是否是socks5://格式
	if !strings.HasPrefix(addr, "socks5://") {
		// 不是socks5://格式，直接返回原地址，不需要认证
		return addr, "", "", false, nil
	}

	// 解析URL
	u, err := url.Parse(addr)
	if err != nil {
		return "", "", "", false, err
	}

	// 获取监听地址
	host := u.Host

	// 检查是否有用户名密码
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
		requireAuth = true
	}

	return host, username, password, requireAuth, nil
}

func (c *Client) ServeForwardSocks5(listen string) error {
	// 解析socks5://格式的地址
	listenAddr, username, password, requireAuth, err := parseSocks5URL(listen)
	if err != nil {
		c.logger.Errorf("Failed to parse SOCKS5 URL: %v\n", err)
		return err
	}

	if requireAuth {
		c.logger.Infof("Listen on %s for SOCKS5 proxy with authentication\n", listenAddr)
	} else {
		c.logger.Infof("Listen on %s for SOCKS5 proxy without authentication\n", listenAddr)
	}

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		c.logger.Errorf("Failed to listen on %s: %v\n", listenAddr, err)
		return err
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			c.logger.Errorf("Error when accept connection: %v.\n", err)
			continue
		}

		go func() {
			// 创建认证信息
			var authInfo *Socks5Auth
			if requireAuth {
				authInfo = &Socks5Auth{Username: username, Password: password}
			}

			c.handleSocks5Client(conn.(*net.TCPConn), authInfo)
		}()
	}
}

func biRelay(left, right connection.HalfOpenConn, logger *logger.Logger) {
	var wg sync.WaitGroup
	wg.Add(1)
	go relay(left, right, &wg, logger, "local <- tunnel")
	wg.Add(1)
	go relay(right, left, &wg, logger, "local -> tunnel")
	wg.Wait()
	logger.Debugf("===========> Close client biRelay")
	_ = left.Close()
	_ = right.Close()
}

// Socks5Auth 存储SOCKS5认证信息
type Socks5Auth struct {
	Username string
	Password string
}

func (c *Client) handleSocks5Client(client *net.TCPConn, auth *Socks5Auth) {
	defer client.Close()
	c.logger.Infof("Accepted SOCKS5 connection from %s\n", client.RemoteAddr())

	// 1. SOCKS5 握手
	if err := c.socks5Handshake(client, auth); err != nil {
		c.logger.Errorf("SOCKS5 handshake error: %v\n", err)
		return
	}

	// 2. 处理SOCKS5请求
	target, err := c.socks5ProcessRequest(client)
	if err != nil {
		c.logger.Errorf("SOCKS5 process request error: %v\n", err)
		return
	}

	c.logger.Infof("SOCKS5 connection from %s to %s\n", client.RemoteAddr(), target)

	// 3. 建立到目标服务器的连接
	connProxy := c.Dial(target)

	// 4. 双向转发数据
	biRelay(client, connProxy, c.logger)
}

func relay(dst, src connection.HalfOpenConn, wg *sync.WaitGroup, logger *logger.Logger, label string) {
	defer wg.Done()
	_, err := io.Copy(dst, src)
	if err != nil {
		_ = dst.SetDeadline(time.Now())
		_ = src.SetDeadline(time.Now())
		_ = dst.Close()
		_ = src.Close()
		if err != io.EOF {
			logger.Errorf("Error when relay client: %v.\n", err)
		}
	} else {
		logger.Debugf("!!!!!!!!!!!!!!!! %s : dst close write", label)
		dst.CloseWrite()
		logger.Debugf("!!!!!!!!!!!!!!!! %s : src close read", label)
		src.CloseRead()
	}
}

// SOCKS5 协议常量
const (
	Socks5Version = 0x05

	// 认证方法
	Socks5AuthNone         = 0x00
	Socks5AuthGSSAPI       = 0x01
	Socks5AuthPassword     = 0x02
	Socks5AuthNoAcceptable = 0xFF

	// 命令类型
	Socks5CmdConnect      = 0x01
	Socks5CmdBind         = 0x02
	Socks5CmdUDPAssociate = 0x03

	// 地址类型
	Socks5AddrTypeIPv4   = 0x01
	Socks5AddrTypeDomain = 0x03
	Socks5AddrTypeIPv6   = 0x04

	// 响应状态
	Socks5ReplySuccess                 = 0x00
	Socks5ReplyGeneralFailure          = 0x01
	Socks5ReplyConnectionNotAllowed    = 0x02
	Socks5ReplyNetworkUnreachable      = 0x03
	Socks5ReplyHostUnreachable         = 0x04
	Socks5ReplyConnectionRefused       = 0x05
	Socks5ReplyTTLExpired              = 0x06
	Socks5ReplyCommandNotSupported     = 0x07
	Socks5ReplyAddressTypeNotSupported = 0x08

	// 用户名密码认证子协议版本
	Socks5AuthPasswordVer = 0x01

	// 用户名密码认证状态
	Socks5AuthStatusSuccess = 0x00
	Socks5AuthStatusFailure = 0x01
)

// SOCKS5握手阶段
func (c *Client) socks5Handshake(conn net.Conn, auth *Socks5Auth) error {
	// 读取客户端支持的认证方法
	buf := make([]byte, 257)
	// 读取版本和方法数量
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return err
	}

	ver, nmethods := buf[0], buf[1]
	if ver != Socks5Version {
		return fmt.Errorf("unsupported SOCKS version: %d", ver)
	}

	// 读取支持的方法列表
	if _, err := io.ReadFull(conn, buf[:nmethods]); err != nil {
		return err
	}

	// 确定认证方法
	var method byte = Socks5AuthNoAcceptable

	// 如果需要用户名密码认证
	if auth != nil {
		// 检查客户端是否支持用户名密码认证
		hasPasswordAuth := false
		for i := 0; i < int(nmethods); i++ {
			if buf[i] == Socks5AuthPassword {
				hasPasswordAuth = true
				break
			}
		}

		if hasPasswordAuth {
			method = Socks5AuthPassword
		} else {
			// 客户端不支持密码认证，但服务器要求密码认证
			conn.Write([]byte{Socks5Version, Socks5AuthNoAcceptable})
			return fmt.Errorf("client doesn't support password authentication")
		}
	} else {
		// 不需要认证，检查客户端是否支持无认证方法
		hasNoAuth := false
		for i := 0; i < int(nmethods); i++ {
			if buf[i] == Socks5AuthNone {
				hasNoAuth = true
				break
			}
		}

		if hasNoAuth {
			method = Socks5AuthNone
		} else {
			// 客户端不支持无认证方法
			conn.Write([]byte{Socks5Version, Socks5AuthNoAcceptable})
			return fmt.Errorf("client doesn't support no authentication")
		}
	}

	// 回复客户端选择的认证方法
	conn.Write([]byte{Socks5Version, method})

	// 如果选择了用户名密码认证，进行认证过程
	if method == Socks5AuthPassword {
		return c.performPasswordAuth(conn, auth)
	}

	return nil
}

// 执行用户名密码认证
func (c *Client) performPasswordAuth(conn net.Conn, auth *Socks5Auth) error {
	if auth == nil {
		return fmt.Errorf("auth info is nil")
	}

	// 读取认证请求
	buf := make([]byte, 513) // 最大用户名长度255 + 最大密码长度255 + 2字节头部

	// 读取版本和用户名长度
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return err
	}

	ver, ulen := buf[0], buf[1]
	if ver != Socks5AuthPasswordVer {
		return fmt.Errorf("unsupported auth version: %d", ver)
	}

	// 读取用户名
	if _, err := io.ReadFull(conn, buf[:ulen]); err != nil {
		return err
	}
	username := string(buf[:ulen])

	// 读取密码长度
	if _, err := io.ReadFull(conn, buf[:1]); err != nil {
		return err
	}
	plen := buf[0]

	// 读取密码
	if _, err := io.ReadFull(conn, buf[:plen]); err != nil {
		return err
	}
	password := string(buf[:plen])

	// 验证用户名和密码
	if username == auth.Username && password == auth.Password {
		// 认证成功
		conn.Write([]byte{Socks5AuthPasswordVer, Socks5AuthStatusSuccess})
		return nil
	} else {
		// 认证失败
		conn.Write([]byte{Socks5AuthPasswordVer, Socks5AuthStatusFailure})
		return fmt.Errorf("invalid username or password")
	}
}

// 处理SOCKS5请求
func (c *Client) socks5ProcessRequest(conn net.Conn) (string, error) {
	buf := make([]byte, 4)

	// 读取版本、命令、保留字段和地址类型
	if _, err := io.ReadFull(conn, buf); err != nil {
		return "", err
	}

	ver, cmd, _, atyp := buf[0], buf[1], buf[2], buf[3]

	if ver != Socks5Version {
		return "", fmt.Errorf("unsupported SOCKS version: %d", ver)
	}

	// 目前只支持CONNECT命令
	if cmd != Socks5CmdConnect {
		c.sendSocks5Reply(conn, Socks5ReplyCommandNotSupported)
		return "", fmt.Errorf("unsupported command: %d", cmd)
	}

	// 解析目标地址
	var host string

	switch atyp {
	case Socks5AddrTypeIPv4:
		// IPv4地址: 4字节
		ipv4 := make([]byte, 4)
		if _, err := io.ReadFull(conn, ipv4); err != nil {
			return "", err
		}
		host = net.IP(ipv4).String()

	case Socks5AddrTypeDomain:
		// 域名: 第一个字节是长度
		domainLen := make([]byte, 1)
		if _, err := io.ReadFull(conn, domainLen); err != nil {
			return "", err
		}

		domain := make([]byte, domainLen[0])
		if _, err := io.ReadFull(conn, domain); err != nil {
			return "", err
		}
		host = string(domain)

	case Socks5AddrTypeIPv6:
		// IPv6地址: 16字节
		ipv6 := make([]byte, 16)
		if _, err := io.ReadFull(conn, ipv6); err != nil {
			return "", err
		}
		host = net.IP(ipv6).String()

	default:
		c.sendSocks5Reply(conn, Socks5ReplyAddressTypeNotSupported)
		return "", fmt.Errorf("unsupported address type: %d", atyp)
	}

	// 读取端口: 2字节，网络字节序(大端)
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		return "", err
	}
	port := int(portBuf[0])<<8 | int(portBuf[1])

	// 构建目标地址
	target := fmt.Sprintf("%s:%d", host, port)

	// 发送成功响应
	if err := c.sendSocks5Reply(conn, Socks5ReplySuccess); err != nil {
		return "", err
	}

	return target, nil
}

// 发送SOCKS5响应
func (c *Client) sendSocks5Reply(conn net.Conn, reply byte) error {
	// 构建响应
	// +----+-----+-------+------+----------+----------+
	// |VER | REP |  RSV  | ATYP | BND.ADDR | BND.PORT |
	// +----+-----+-------+------+----------+----------+
	// | 1  |  1  | X'00' |  1   | Variable |    2     |
	// +----+-----+-------+------+----------+----------+

	// 使用IPv4地址0.0.0.0和端口0作为绑定地址
	response := []byte{
		Socks5Version,
		reply,
		0x00, // 保留字段
		Socks5AddrTypeIPv4,
		0, 0, 0, 0, // IP地址 (0.0.0.0)
		0, 0, // 端口 (0)
	}

	_, err := conn.Write(response)
	return err
}
