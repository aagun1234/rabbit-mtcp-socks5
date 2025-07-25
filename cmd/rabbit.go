package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/aagun1234/rabbit-mtcp-socks5/client"
	"github.com/aagun1234/rabbit-mtcp-socks5/connection"
	"github.com/aagun1234/rabbit-mtcp-socks5/connection_pool"
	"github.com/aagun1234/rabbit-mtcp-socks5/logger"
	"github.com/aagun1234/rabbit-mtcp-socks5/peer"
	"github.com/aagun1234/rabbit-mtcp-socks5/server"
	"github.com/aagun1234/rabbit-mtcp-socks5/stats"
	"github.com/aagun1234/rabbit-mtcp-socks5/tunnel"
	"github.com/aagun1234/rabbit-mtcp-socks5/tunnel_pool"
	"gopkg.in/yaml.v3"
)

var Version = "1.0.10 tcp/socks5" //"No version information"

var (
	DialTimeoutSec    = 6
	ReconnectDelaySec = 5
	MaxRetries        = 3
	// 全局连接池，用于在statusServer中获取连接信息
	ClientConnectionPool *connection_pool.ConnectionPool
	ServerConnectionPool *connection_pool.ConnectionPool
	// 服务端 PeerGroup，用于获取所有 ServerPeer 的连接池
	ServerPeerGroup *peer.PeerGroup
)

const (
	ClientMode = iota
	ServerMode
	DefaultPassword = "PASSWORD"
)

type Config struct {
	Mode       string   `yaml:"mode"` // 运行模式: "client" 或 "server"
	mode       int      `yaml:"-"`
	configFile string   `yaml:"-"`           // 配置文件路径 (不写入YAML)
	AppName    string   `yaml:"appname"`     // 日志中标识
	Verbose    int      `yaml:"verbose"`     // 日志级别: 1-5`
	RabbitAddr []string `yaml:"rabbit-addr"` // 服务端WebSocket URL列表 (例如: ["ws://server1:8081/tunnel", "wss://server2:8082/tunnel"])
	Password   string   `yaml:"password"`    //加密用
	AEADCipher string   `yaml:"aead-cipher"` //加密用,CHACHA20-IETF-POLY1305,AES-128-GCM,AES-192-GCM,AES-256-GCM
	// Client 模式配置
	Listen  string `yaml:"listen"`  // 客户端侦听的本地TCP地址 (例如: "127.0.0.1:1080"或socks5://127.0.0.1:1080)
	Dest    string `yaml:"dest"`    // 目标服务
	TunnelN int    `yaml:"tunnelN"` //客户端发起的连接数
	//	AuthKey         string `yaml:"authkey"`      // 认证密钥
	//	TLSCertFile     string `yaml:"tls-certfile"` // 服务端证书文件路径
	//	TLSKeyFile      string `yaml:"tls-keyfile"`  // 服务端密钥文件路径
	//	Insecure        bool   `yaml:"insecure"`     // 客户端是否跳过服务端证书验证InsecureSkipVerify
	UseSyslog       bool `yaml:"use-syslog"`   // 客户端是否跳过服务端证书验证InsecureSkipVerify
	RetryFailedAddr bool `yaml:"retry-failed"` // 对于客户端连接失败的rabbit-addr，是否反复重试，如果否，则不会重试连接，直到所有的都连不上

	StatusServer string `yaml:"status-server"` // 状态服务侦听的本地TCP地址 (例如: "127.0.0.1:8010")
	StatusACL    string `yaml:"status-acl"`    // 状态服务ACL

	PingIntervalSec int `yaml:"ping-interval"` // ping间隔 30

	DialTimeoutSec          int `yaml:"dial-timeout"`     // 拨号超时时间 6
	RecvTimeoutSec          int `yaml:"recv-timeout"`     // 应答超时 20
	PacketWaitTimeoutSec    int `yaml:"buffer-timeout"`   // 缓存序号超时 7
	ReconnectDelaySec       int `yaml:"reconnect-delay"`  // 重连间隔   5
	OutboundBlockTimeoutSec int `yaml:"outblock-timeout"` // If block processor is waiting for a "hole", and no packet comes within this limit, the Connection will be closed 3
	MaxRetries              int `yaml:"max-retries"`      // 连接重试最大次数

	OrderedRecvQueueSize   int `yaml:"order-rqueue-size"` // 32  OrderedRecvQueue channe
	SendQueueSize          int `yaml:"squeue-size"`       //32
	RecvQueueSize          int `yaml:"rqueue-size"`       //32
	OutboundRecvBufferSize int `yaml:"recv-buffersize"`   //32 * 1024
}

// NewDefaultConfig 返回一个默认配置实例
func NewDefaultConfig() *Config {
	return &Config{
		Mode:       "client",
		AppName:    "rabbit-mtcp-ws",
		Verbose:    4,
		RabbitAddr: []string{"ws://127.0.0.1:443/tunnel"},
		Password:   "PASSWORD",
		AEADCipher: "CHACHA20-IETF-POLY1305",
		Listen:     "127.0.0.1:1080",
		Dest:       "",
		TunnelN:    4,
		//		AuthKey:                 "",
		//		TLSCertFile:             "",
		//		TLSKeyFile:              "",
		//		Insecure:                true,
		UseSyslog:               true,
		RetryFailedAddr:         true,
		PingIntervalSec:         30,
		DialTimeoutSec:          6,
		RecvTimeoutSec:          20,
		PacketWaitTimeoutSec:    8,
		ReconnectDelaySec:       5,
		OutboundBlockTimeoutSec: 3,
		MaxRetries:              3,
		OrderedRecvQueueSize:    32,
		SendQueueSize:           32,
		RecvQueueSize:           32,
		OutboundRecvBufferSize:  32 * 1024,
		StatusServer:            "",
		StatusACL:               "",
	}
}

// LoadConfig 从命令行参数和YAML文件加载配置
// 优先级：默认值 -> YAML文件 -> 命令行参数
func LoadConfig() (*Config, error) {
	cfg := NewDefaultConfig() // 1. 加载默认配置

	// 创建一个临时的FlagSet来解析命令行参数
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	var (
		modeArg       string
		appNameArg    string
		configFileArg string
		verboseArg    int
		rabbitAddrArg string
		passwordArg   string
		aeadCipherArg string
		listenArg     string
		destArg       string
		tunnelNArg    int
		//		authKeyArg                 string
		//		tlsCertFileArg             string
		//		tlsKeyFileArg              string
		//		insecureArg                bool
		useSyslogArg               bool
		retryFailedAddrArg         bool
		statusServerArg            string
		statusACLArg               string
		pingIntervalSecArg         int
		dialTimeoutSecArg          int
		recvTimeoutSecArg          int
		packetWaitTimeoutSecArg    int
		reconnectDelaySecArg       int
		outboundBlockTimeoutSecArg int
		maxRetriesArg              int
		orderedRecvQueueSizeArg    int
		sendQueueSizeArg           int
		recvQueueSizeArg           int
		outboundRecvBufferSizeArg  int

		printVersion1 bool
		printVersion  bool
	)

	// 定义所有命令行参数，将它们绑定到临时变量
	// 注意：这里将flag的默认值设为空字符串/0/false，以便通过flagsSeen map判断是否被设置
	fs.StringVar(&configFileArg, "c", "", "Path to configuration file (YAML)")
	fs.StringVar(&modeArg, "mode", "", "running mode(s or c)")
	fs.StringVar(&appNameArg, "appname", "", "Application name in syslog")
	fs.IntVar(&verboseArg, "verbose", 0, "verbose level(0~6)")
	fs.StringVar(&rabbitAddrArg, "rabbit-addr", "", "Comma-separated list of server WebSocket URLs")
	fs.StringVar(&aeadCipherArg, "aead-cipher", "", "aead-cipher, CHACHA20-IETF-POLY1305,AES-128-GCM,AES-192-GCM,AES-256-GCM, default: CHACHA20-IETF-POLY1305")
	fs.StringVar(&passwordArg, "password", "", "password")
	fs.StringVar(&listenArg, "listen", "", "[Client Only] listen address, eg: 127.0.0.1:2333 or socks5://127.0.0.1:1080")
	fs.StringVar(&destArg, "dest", "", "[Client Only] destination address, eg: shadowsocks server address")
	fs.IntVar(&tunnelNArg, "tunnelN", 0, "[Client Only] number of tunnels to use in rabbit-tcp")
	//	fs.StringVar(&authKeyArg, "authkey", "", "Websocket authkey, eg: mysecret")
	//	fs.StringVar(&tlsCertFileArg, "tls-certfile", "", "[Server Only] TLS cert file path, eg: /root/server.crt")
	//	fs.StringVar(&tlsKeyFileArg, "tls-keyfile", "", "[Server Only] TLS key file path, eg: /root/server.key")
	//	fs.BoolVar(&insecureArg, "insecure", false, "InsecureSkipVerify")
	fs.BoolVar(&useSyslogArg, "use-syslog", false, "Write to systemlog")
	fs.BoolVar(&retryFailedAddrArg, "retry-failed", false, "[Client Only] retry failed rabbit-addr")
	fs.StringVar(&statusServerArg, "status-server", "", "Sataus server listen address")
	fs.StringVar(&statusACLArg, "status-acl", "", "Status server ACL")
	fs.IntVar(&pingIntervalSecArg, "ping-interval", 0, "Ping-pong interval, default 30(seconds)")
	fs.IntVar(&dialTimeoutSecArg, "dial-timeout", 0, "Dial timeout, default 6(seconds)")
	fs.IntVar(&recvTimeoutSecArg, "recv-timeout", 0, "Packet receive read timeout, default 20(seconds)")
	fs.IntVar(&packetWaitTimeoutSecArg, "packet-timeout", 0, "Inbound packet sequence waiting timeout, default 8(seconds)")
	fs.IntVar(&reconnectDelaySecArg, "reconnect-delay", 0, "Delay between reconnects, default 5(seconds)")
	fs.IntVar(&outboundBlockTimeoutSecArg, "outbound-timeout", 0, "Outbound packet timeout, default 3(seconds)")
	fs.IntVar(&maxRetriesArg, "max-retries", 0, "Max retries not implented")
	fs.IntVar(&orderedRecvQueueSizeArg, "order-rqueue-size", 0, "Ordered receive queue size, default 32")
	fs.IntVar(&sendQueueSizeArg, "squeue-size", 0, "Send queue size, default 32")
	fs.IntVar(&recvQueueSizeArg, "rqueue-size", 0, "Receive queue size, default 32")
	fs.IntVar(&outboundRecvBufferSizeArg, "buffer-size", 0, "Receive BufferSize, default 32*1024")

	fs.BoolVar(&printVersion, "version", false, "show version")
	fs.BoolVar(&printVersion1, "v", false, "show version")

	fs.Parse(os.Args[1:]) // 2. 解析命令行参数，它们会填充到上面的临时变量中

	// version
	if printVersion || printVersion1 {
		log.Println("Rabbit TCP ws (https://github.com/aagun1234/rabbit-mtcp-socks5/)")
		//		log.Println("Websocket version of Rabbit TCP (https://github.com/ihciah/rabbit-tcp)")
		log.Printf("Version: %s.\n", Version)
		return nil, nil
	}

	// 记录哪些命令行参数被显式设置了
	flagsSeen := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		flagsSeen[f.Name] = true
	})

	// 3. 读取YAML文件并合并 (YAML覆盖默认值)
	configFilePath := configFileArg
	if configFilePath == "" {
		// 尝试在默认路径下查找名为 "config.yaml" 的文件
		homeDir, _ := os.UserHomeDir()
		possiblePaths := []string{
			"config.yaml", // 当前目录
			fmt.Sprintf("%s/.rabbit/config.yaml", homeDir), // 用户主目录
			"/etc/rabbit/config.yaml",                      // Linux 系统常见配置目录
		}
		for _, p := range possiblePaths {
			if _, err := os.Stat(p); err == nil {
				configFilePath = p
				break
			}
		}
	}

	if configFilePath != "" {
		fileContent, err := os.ReadFile(configFilePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read config file %s: %w", configFilePath, err)
		}
		// Unmarshal directly into `cfg` (它已经包含默认值)
		if err := yaml.Unmarshal(fileContent, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config file %s: %w", configFilePath, err)
		}
	}

	// 4. 命令行参数覆盖YAML文件和默认值
	// 检查每个参数是否在命令行中被显式设置了，如果是，则用命令行值覆盖
	if flagsSeen["mode"] {
		cfg.Mode = strings.ToLower(modeArg)
	}
	cfg.Mode = strings.ToLower(cfg.Mode)
	if flagsSeen["verbose"] {
		cfg.Verbose = verboseArg
	}
	if flagsSeen["appname"] {
		cfg.AppName = appNameArg
	}
	if flagsSeen["rabbit-addr"] {
		cfg.RabbitAddr = splitAndTrim(rabbitAddrArg, ",")
	}
	if flagsSeen["aead-cipher"] {
		cfg.AEADCipher = aeadCipherArg
	}
	if flagsSeen["password"] {
		cfg.Password = passwordArg
	}
	if flagsSeen["listen"] {
		cfg.Listen = listenArg
	}
	if flagsSeen["dest"] {
		cfg.Dest = destArg
	}
	if flagsSeen["tunnelN"] {
		cfg.TunnelN = tunnelNArg
	}
	//	if flagsSeen["authkey"] {
	//		cfg.AuthKey = authKeyArg
	//	}
	//	if flagsSeen["tls-certfile"] {
	//		cfg.TLSCertFile = tlsCertFileArg
	//	}
	//	if flagsSeen["tls-keyfile"] {
	//		cfg.TLSKeyFile = tlsKeyFileArg
	//	}
	//	if flagsSeen["insecure"] {
	//		cfg.Insecure = insecureArg
	//	}
	if flagsSeen["retry-failed"] {
		cfg.RetryFailedAddr = retryFailedAddrArg
	}
	if flagsSeen["status-server"] {
		cfg.StatusServer = statusServerArg
	}
	if flagsSeen["status-acl"] {
		cfg.StatusACL = statusACLArg
	}
	if flagsSeen["ping-interval"] {
		cfg.PingIntervalSec = pingIntervalSecArg
	}
	if flagsSeen["dial-timeout"] {
		cfg.DialTimeoutSec = dialTimeoutSecArg
	}
	if flagsSeen["recv-timeout"] {
		cfg.RecvTimeoutSec = recvTimeoutSecArg
	}
	if flagsSeen["packet-timeout"] {
		cfg.PacketWaitTimeoutSec = packetWaitTimeoutSecArg
	}
	if flagsSeen["reconnect-delay"] {
		cfg.ReconnectDelaySec = reconnectDelaySecArg
	}
	if flagsSeen["outbound-timeout"] {
		cfg.OutboundBlockTimeoutSec = outboundBlockTimeoutSecArg
	}
	if flagsSeen["max-retries"] {
		cfg.MaxRetries = maxRetriesArg
	}
	if flagsSeen["order-rqueue-size"] {
		cfg.OrderedRecvQueueSize = orderedRecvQueueSizeArg
	}
	if flagsSeen["squeue-size"] {
		cfg.SendQueueSize = sendQueueSizeArg
	}
	if flagsSeen["rqueue-size"] {
		cfg.RecvQueueSize = recvQueueSizeArg
	}
	if flagsSeen["buffer-size"] {
		cfg.OutboundRecvBufferSize = outboundRecvBufferSizeArg
	}

	return cfg, nil
}

func parseFlags() (pass bool, cfg *Config) {
	// mode int, password string, addr []string, listen string, dest string, authkey, keyfile, crtfile string, tunnelN int, verbose int, insecure bool, appname string, usesyslog bool, retryfailed bool) {

	var err error
	cfg, err = LoadConfig()
	if err != nil {
		fmt.Printf("Failed to load configuration: %v\n", err)
		os.Exit(1)
	}
	if cfg == nil {
		os.Exit(0)
	}

	pass = true

	// mode

	connection.OrderedRecvQueueSize = cfg.OrderedRecvQueueSize
	connection.RecvQueueSize = cfg.RecvQueueSize
	connection.OutboundRecvBuffer = cfg.OutboundRecvBufferSize
	connection.OutboundBlockTimeoutSec = cfg.OutboundBlockTimeoutSec
	connection.PacketWaitTimeoutSec = cfg.PacketWaitTimeoutSec
	connection.DialTimeoutSec = cfg.DialTimeoutSec
	tunnel_pool.DialTimeoutSec = cfg.DialTimeoutSec
	tunnel_pool.ReconnectDelaySec = cfg.ReconnectDelaySec
	tunnel_pool.MaxRetries = cfg.MaxRetries
	tunnel_pool.TunnelBlockTimeoutSec = cfg.OutboundBlockTimeoutSec
	tunnel_pool.ErrorWaitSec = cfg.ReconnectDelaySec
	tunnel_pool.PingInterval = cfg.PingIntervalSec
	//tunnel_pool.SendQueueSize          = cfg.SendQueueSize
	tunnel_pool.RecvQueueSize = cfg.RecvQueueSize
	tunnel_pool.TunnelRecvTimeoutSec = cfg.RecvTimeoutSec

	if cfg.Mode == "c" || cfg.Mode == "client" {
		cfg.mode = ClientMode
	} else if cfg.Mode == "s" || cfg.Mode == "server" {
		cfg.mode = ServerMode
	} else {
		log.Printf("Unsupported mode %s.\n", cfg.Mode)
		pass = false
		return
	}

	// password
	if cfg.Password == "" {
		log.Println("Password must be specified.")
		pass = false
		return
	}
	if cfg.Password == DefaultPassword {
		log.Println("Password must be changed instead of default password.")
		pass = false
		return
	}

	// listen, dest, tunnelN
	if cfg.mode == ClientMode {
		if cfg.Listen == "" {
			log.Println("Listen address must be specified in client mode.")
			pass = false
		}
		// 如果不是SOCKS5模式，则需要指定dest参数
		if !strings.HasPrefix(cfg.Listen, "socks5://") && cfg.Dest == "" {
			log.Println("Destination address must be specified in TCP forward mode.")
			pass = false
		}
		if cfg.TunnelN == 0 {
			log.Println("Tunnel number must be positive.")
			pass = false
		}
	}

	//addr = strings.Split(rabbitaddr, ",")
	return
}

// IsIPInSubnets 检查 IP 是否在给定的子网列表中
func IsIPInSubnets(ipStr string, subnets []string) (bool, error) {

	if len(subnets) <= 0 {
		return true, nil
	}

	// 处理带端口的 IP（如 "192.168.1.1:8080"）
	ipStr, _, err := net.SplitHostPort(ipStr)
	if err != nil {
		// 如果没有端口（如直接是 "192.168.1.1"），继续解析
		ipStr = strings.TrimSpace(ipStr)
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false, err
	}

	for _, subnet := range subnets {

		if strings.Contains(subnet, ",") {
			subnets1 := strings.Split(subnet, ",")
			for _, subnet1 := range subnets1 {
				_, cidr, err := net.ParseCIDR(subnet1)
				if err != nil {
					log.Printf("[main] Check ACL error: %s", err)
					continue
				}

				if cidr.Contains(ip) {
					return true, nil
				}
			}
		} else {
			_, cidr, err := net.ParseCIDR(subnet)
			if err != nil {
				log.Printf("[main] Check ACL error: %s", err)
				return false, err
			}

			if cidr.Contains(ip) {
				return true, nil
			}
		}
	}

	return false, nil
}

func statusServer1(listen, acl string, cfg *Config, c *client.Client) {
	statlogger := logger.NewLogger("[StatusServer]")
	acls := strings.Split(acl, ",")
	if len(acls) == 1 {
		if acls[0] == "" {
			acls[0] = "0.0.0.0/0"
		}
	}
	// 解析基本认证信息
	var username, password string
	if atIndex := strings.Index(listen, "@"); atIndex > 0 {
		authPart := listen[:atIndex]
		listen = listen[atIndex+1:]
		if colonIndex := strings.Index(authPart, ":"); colonIndex > 0 {
			username = authPart[:colonIndex]
			password = authPart[colonIndex+1:]
		}
	}

	// 创建带基本认证的handler
	handler := http.NewServeMux()
	handler.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		statlogger.InfoAf("%s request for config", ip)
		isAllowed, err := IsIPInSubnets(ip, acls)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			statlogger.Errorf("Error matching %s with ACL: %v", ip, err)
			return
		}
		if !isAllowed {
			w.WriteHeader(http.StatusUnauthorized)
			statlogger.Errorf("%s blocked by ACL", ip)
			return
		}

		if username != "" && !checkBasicAuth(r, username, password) {
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
			w.WriteHeader(http.StatusUnauthorized)
			statlogger.Errorf("%s blocked by Basic Auth", ip)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg)
	})

	handler.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		statlogger.InfoAf("%s request status config", ip)
		isAllowed, err := IsIPInSubnets(ip, acls)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			statlogger.Errorf("Error matching %s with ACL: %v", ip, err)
			return
		}
		if !isAllowed {
			w.WriteHeader(http.StatusUnauthorized)
			statlogger.Errorf("%s blocked by ACL", ip)
			return
		}
		if username != "" && !checkBasicAuth(r, username, password) {
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
			w.WriteHeader(http.StatusUnauthorized)
			statlogger.Errorf("%s blocked by Basic Auth", ip)
			return
		}

		// 获取统计数据
		var statsData map[string]interface{}
		if cfg.mode == ClientMode {
			statsData = stats.ClientStats.GetStats()
		} else {
			statsData = stats.ServerStats.GetStats()
		}
		statlogger.Debugf("Data: %v", statsData)

		// 获取连接信息
		var connectionsInfo []map[string]interface{}
		var tunnelsInfo []map[string]interface{}
		connectionsInfo = make([]map[string]interface{}, 0, 1)
		tunnelsInfo = make([]map[string]interface{}, 0, 1)
		if cfg.mode == ClientMode {
			ClientConnectionPool = c.Peer.GetConnectionPool()
			statlogger.Debugf("ClientConnectionPool: %v", ClientConnectionPool)
			// 客户端模式：从单一连接池获取连接信息
			connectionsInfo = ClientConnectionPool.GetConnectionsInfo()
			statlogger.Debugf("connectionsInfo: %v", connectionsInfo)
			tunnelsInfo = ClientConnectionPool.GetTunnelsInfo()
			statlogger.Debugf("tunnelsInfo: %v", tunnelsInfo)
		}

		// 合并基本状态和统计数据
		status := map[string]interface{}{
			"running":     true,
			"mode":        cfg.Mode,
			"version":     Version,
			"stats":       statsData,
			"connections": connectionsInfo,
			"tunnels":     tunnelsInfo,
			"tunnelPool":  ClientConnectionPool.GetTunnelPoolInfo(),
		}
		w.Header().Set("Content-Type", "application/json")
		statlogger.Debugf("Status: %v", status)
		json.NewEncoder(w).Encode(status)
	})

	statlogger.Infof("Status server listening on %s", listen)
	if err := http.ListenAndServe(listen, handler); err != nil {
		statlogger.Errorf("Status server error: %v", err)
	}
}

func statusServer2(listen, acl string, cfg *Config, s *server.Server) {
	statlogger := logger.NewLogger("[StatusServer]")
	acls := strings.Split(acl, ",")
	if len(acls) == 1 {
		if acls[0] == "" {
			acls[0] = "0.0.0.0/0"
		}
	}
	// 解析基本认证信息
	var username, password string
	if atIndex := strings.Index(listen, "@"); atIndex > 0 {
		authPart := listen[:atIndex]
		listen = listen[atIndex+1:]
		if colonIndex := strings.Index(authPart, ":"); colonIndex > 0 {
			username = authPart[:colonIndex]
			password = authPart[colonIndex+1:]
		}
	}

	// 创建带基本认证的handler
	handler := http.NewServeMux()
	handler.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		statlogger.InfoAf("%s request for config", ip)
		isAllowed, err := IsIPInSubnets(ip, acls)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			statlogger.Errorf("Error matching %s with ACL: %v", ip, err)
			return
		}
		if !isAllowed {
			w.WriteHeader(http.StatusUnauthorized)
			statlogger.Errorf("%s blocked by ACL", ip)
			return
		}

		if username != "" && !checkBasicAuth(r, username, password) {
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
			w.WriteHeader(http.StatusUnauthorized)
			statlogger.Errorf("%s blocked by Basic Auth", ip)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg)
	})

	handler.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		statlogger.InfoAf("%s request status config", ip)
		isAllowed, err := IsIPInSubnets(ip, acls)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			statlogger.Errorf("Error matching %s with ACL: %v", ip, err)
			return
		}
		if !isAllowed {
			w.WriteHeader(http.StatusUnauthorized)
			statlogger.Errorf("%s blocked by ACL", ip)
			return
		}
		if username != "" && !checkBasicAuth(r, username, password) {
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
			w.WriteHeader(http.StatusUnauthorized)
			statlogger.Errorf("%s blocked by Basic Auth", ip)
			return
		}

		// 获取统计数据
		var statsData map[string]interface{}
		if cfg.mode == ClientMode {
			statsData = stats.ClientStats.GetStats()
		} else {
			statsData = stats.ServerStats.GetStats()
		}
		statlogger.Debugf("Data: %v", statsData)

		// 获取连接信息
		var connectionsInfo []map[string]interface{}
		var tunnelsInfo []map[string]interface{}
		var tunnelPoolsInfo []map[string]interface{}
		connectionsInfo = make([]map[string]interface{}, 0)
		tunnelsInfo = make([]map[string]interface{}, 0)
		tunnelPoolsInfo = make([]map[string]interface{}, 0)
		if cfg.mode == ServerMode {
			ServerPeerGroup = s.GetPeerGroup()
			// 服务端模式：从所有 ServerPeer 的连接池获取连接信息

			connectionPools := ServerPeerGroup.GetAllConnectionPools()
			statlogger.Debugf("Length of connectionPools: %d", len(connectionPools))
			tunnelPools := ServerPeerGroup.GetAllTunnelPools()
			// 遍历所有连接池，获取连接信息
			for _, pool := range connectionPools {
				connectionsInfo = append(connectionsInfo, pool.GetConnectionsInfo()...)
			}
			for _, tpool := range tunnelPools {
				tunnelPoolsInfo = append(tunnelPoolsInfo, tpool.GetTunnelPoolInfo())
				tunnelsInfo = append(tunnelsInfo, tpool.GetTunnelConnsInfo()...)
			}
		}

		// 合并基本状态和统计数据
		status := map[string]interface{}{
			"running":     true,
			"mode":        cfg.Mode,
			"version":     Version,
			"stats":       statsData,
			"connections": connectionsInfo,
			"tunnels":     tunnelsInfo,
			"tunnelpools": tunnelPoolsInfo,
		}
		w.Header().Set("Content-Type", "application/json")
		statlogger.Debugf("Status: %v", status)
		json.NewEncoder(w).Encode(status)
	})

	statlogger.Infof("Status server listening on %s", listen)
	if err := http.ListenAndServe(listen, handler); err != nil {
		statlogger.Errorf("Status server error: %v", err)
	}
}

func checkBasicAuth(r *http.Request, username, password string) bool {
	user, pass, ok := r.BasicAuth()
	return ok && user == username && pass == password
}

func main() {
	pass, mcfg := parseFlags()
	if !pass {
		return
	}
	logger.LEVEL = mcfg.Verbose
	logger.AppName = mcfg.AppName
	logger.UseSyslog = mcfg.UseSyslog
	mainlogger := logger.NewLogger("[Main]")

	mainlogger.Debugf("mode: %v, password: %v, addr: %v, listen: %v, dest: %v, tunnelN: %v, verbose: %v\n", mcfg.Mode, mcfg.Password, mcfg.RabbitAddr, mcfg.Listen, mcfg.Dest, mcfg.TunnelN, mcfg.Verbose)
	cipher, _ := tunnel.NewAEADCipher(mcfg.AEADCipher, nil, mcfg.Password)

	// 初始化统计模块，使用20秒的历史窗口
	stats.InitStats(20)

	if mcfg.mode == ClientMode {
		c := client.NewClient(mcfg.TunnelN, mcfg.RabbitAddr, cipher, "", true, mcfg.RetryFailedAddr)

		// 使用 GetConnectionPool 方法获取连接池
		ClientConnectionPool = c.Peer.GetConnectionPool()

		if mcfg.StatusServer != "" {
			go statusServer1(mcfg.StatusServer, mcfg.StatusACL, mcfg, &c)
			mainlogger.Infof("Starting status server with address: %s\n", mcfg.StatusServer)
		}

		// 检查listen参数是否以socks5://开头
		if strings.HasPrefix(mcfg.Listen, "socks5://") {
			mainlogger.Infof("Starting SOCKS5 proxy with address: %s\n", mcfg.Listen)
			c.ServeForwardSocks5(mcfg.Listen)
		} else {
			mainlogger.Infof("Starting TCP forward from %s to %s\n", mcfg.Listen, mcfg.Dest)
			c.ServeForward(mcfg.Listen, mcfg.Dest)
		}
	} else {

		s := server.NewServer(cipher, "", "", "")

		if mcfg.StatusServer != "" {
			go statusServer2(mcfg.StatusServer, mcfg.StatusACL, mcfg, &s)
			mainlogger.Infof("Starting status server with address: %s\n", mcfg.StatusServer)
		}
		// 保存服务端的 PeerGroup 到全局变量，用于在 statusServer 中获取连接信息
		// 由于服务端可能有多个连接池（每个 ServerPeer 一个），我们需要通过 PeerGroup 获取所有连接池
		ServerPeerGroup = s.GetPeerGroup()
		mainlogger.Infof("Starting server with address: %s\n", mcfg.RabbitAddr)
		s.Serve(mcfg.RabbitAddr)
	}
}

// 辅助函数：分割字符串并去除空白
func splitAndTrim(s, sep string) []string {
	if s == "" { // 处理空字符串情况，避免返回 [""]
		return []string{}
	}
	parts := strings.Split(s, sep)
	trimmedParts := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			trimmedParts = append(trimmedParts, trimmed)
		}
	}
	return trimmedParts
}

// 辅助函数：简化版 strings.Split
func split(s, sep string) []string {
	var result []string
	idx := 0
	for {
		i := find(s[idx:], sep)
		if i == -1 {
			result = append(result, s[idx:])
			break
		}
		result = append(result, s[idx:idx+i])
		idx += i + len(sep)
	}
	return result
}

// 辅助函数：简化版 strings.TrimSpace
func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

// 辅助函数：简化版 strings.Index
func find(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			if s[i+j] != substr[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
