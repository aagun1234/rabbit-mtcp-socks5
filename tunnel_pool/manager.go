package tunnel_pool

import (
	"context"
	"net"

	"github.com/aagun1234/rabbit-mtcp-socks5/logger"
	"github.com/aagun1234/rabbit-mtcp-socks5/tunnel"

	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"
	"time"

	//"github.com/gorilla/websocket"
	"go.uber.org/atomic"
)

type Manager interface {
	Notify(pool *TunnelPool)         // When TunnelPool size changed, Notify should be called
	DecreaseNotify(pool *TunnelPool) // When TunnelPool size decreased, DecreaseNotify should be called
}

type ClientManager struct {
	decreaseNotifyLock sync.Mutex // Only one decrease notify can run at the same time
	tunnelNum          int
	endpoints          []string
	peerID             uint32
	cipher             tunnel.Cipher
	logger             *logger.Logger
	authkey            string
	insecure           bool
	retryFailed        bool
}
type ClientCFG struct {
	tunnelNum   int
	endpoints   []string
	authkey     string
	insecure    bool
	retryFailed bool
}

func NewClientManager(tunnelNum int, endpoints []string, peerID uint32, cipher tunnel.Cipher, authkey string, insecure bool, retryfailed bool) ClientManager {
	return ClientManager{
		tunnelNum:   tunnelNum,
		endpoints:   endpoints,
		cipher:      cipher,
		peerID:      peerID,
		logger:      logger.NewLogger("[ClientManager]"),
		authkey:     authkey,
		insecure:    insecure,
		retryFailed: retryfailed,
	}
}

// TLSConfigFromFiles 从文件加载 TLS 配置
func TLSConfigFromFiles(certFile, keyFile, caFile string, insecureSkipVerify bool) (*tls.Config, error) {
	var tlsConfig tls.Config
	//var err error

	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load TLS key pair: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	if caFile != "" {
		caCert, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate: %w", err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to append CA certificate to pool")
		}
		tlsConfig.RootCAs = caCertPool                     // For client to verify server
		tlsConfig.ClientCAs = caCertPool                   // For server to verify client (mutual TLS)
		tlsConfig.ClientAuth = tls.VerifyClientCertIfGiven // Or tls.RequireAndVerifyClientCert
	}

	tlsConfig.InsecureSkipVerify = insecureSkipVerify // For client only
	tlsConfig.MinVersion = tls.VersionTLS12           // 推荐最低 TLS 1.2
	return &tlsConfig, nil
}

// Keep tunnelPool size above tunnelNum
func (cm *ClientManager) DecreaseNotify(pool *TunnelPool) {
	cm.decreaseNotifyLock.Lock()
	defer cm.decreaseNotifyLock.Unlock()
	tunnelCount := len(pool.tunnelMapping)
	lastfailed := ""
	retryweight := make(map[string]int, len(cm.endpoints))
	for tunnelToCreate := cm.tunnelNum - tunnelCount; tunnelToCreate > 0; {

		select {
		case <-pool.ctx.Done():
			// Have to return if pool cancel is called.
			return
		default:
		}
		endpoint := ""
		curindex := 0
		if len(cm.endpoints) > 0 {
			curindex = (tunnelToCreate - 1) % len(cm.endpoints)
			endpoint = cm.endpoints[curindex]
		} else {
			endpoint = ""
			curindex = 0
		}
		if endpoint != "" {
			cm.logger.Debugf("Need %d new tunnels to %s now.\n", tunnelToCreate, endpoint)
			dialTimeout := time.Duration(DialTimeoutSec) * time.Second
			conn, err := net.DialTimeout("tcp", endpoint, dialTimeout)
			//conn, err := net.Dial("tcp", endpoint) //cm.endpoint)
			if err != nil {
				cm.logger.Errorf("Error when dial to %s: %v.\n", endpoint, err)

				if cm.retryFailed {
					if retryweight[endpoint] < 5 {
						retryweight[endpoint]++
					}
				} else {
					if lastfailed == endpoint { // if second time fail to endpoint
						if curindex == (len(cm.endpoints) - 1) { // move last endpoint to first
							cm.endpoints[curindex] = cm.endpoints[0]
							cm.endpoints[0] = endpoint
						} else {
							// going to reconnect to last success endpoint, move current failed endpoint to an older position
							cm.endpoints[curindex] = cm.endpoints[curindex+1]
							cm.endpoints[curindex+1] = endpoint
						}
						cm.logger.Errorf(" %s moved to last.\n", endpoint)
					}
					retryweight[endpoint] = 1
				}
				time.Sleep(time.Duration(retryweight[endpoint]*ErrorWaitSec) * time.Second)
				lastfailed = endpoint
				continue

			}
			if lastfailed == endpoint { //last failed successed, reset
				lastfailed = ""
				retryweight[endpoint] = 0
			}
			tun, err := NewActiveTunnel(conn, cm.cipher, cm.peerID) //创建一个tunnel并交换ID（握手）
			if err != nil {
				cm.logger.Errorf("Error when create active tunnel: %v\n", err)
				time.Sleep(time.Duration(ErrorWaitSec) * time.Second)
				continue
			}
			cm.logger.Debugf("ClientManager DecreaseNotify Set ReadDeadLine unlimit.\n")
			conn.SetReadDeadline(time.Time{})
			// 移除这里的 PingPong 调用，让它只在 pool.AddTunnel 中启动
			pool.AddTunnel(&tun)
			tunnelToCreate--
			cm.logger.Infof("Successfully dialed to %s. ", endpoint)
		}
	}
}

func (cm *ClientManager) Notify(pool *TunnelPool) {}

type ServerManager struct {
	notifyLock          sync.Mutex // Only one notify can run in the same time
	removePeerFunc      context.CancelFunc
	cancelCountDownFunc context.CancelFunc
	triggered           atomic.Bool
	logger              *logger.Logger
}

func NewServerManager(removePeerFunc context.CancelFunc) ServerManager {
	return ServerManager{
		logger:         logger.NewLogger("[ServerManager]"),
		removePeerFunc: removePeerFunc,
	}
}

// If tunnelPool size is zero for more than EmptyPoolDestroySec, delete it
func (sm *ServerManager) Notify(pool *TunnelPool) {
	tunnelCount := len(pool.tunnelMapping)

	if tunnelCount == 0 && sm.triggered.CAS(false, true) {
		var destroyAfterCtx context.Context
		destroyAfterCtx, sm.cancelCountDownFunc = context.WithCancel(context.Background())
		go func(*ServerManager) {
			select {
			case <-destroyAfterCtx.Done():
				sm.logger.Debugln("ServerManager notify canceled.")
			case <-time.After(time.Duration(EmptyPoolDestroySec) * time.Second):
				sm.logger.InfoAln("ServerManager will be destroyed.")
				sm.removePeerFunc()
			}
		}(sm)
	}

	if tunnelCount != 0 && sm.triggered.CAS(true, false) {
		sm.cancelCountDownFunc()
	}
}

func (sm *ServerManager) DecreaseNotify(pool *TunnelPool) {}
