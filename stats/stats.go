package stats

import (
	"sync"
	"sync/atomic"
	"time"
)

// Stats 结构体用于存储和计算统计数据
type Stats struct {
	mutex           sync.RWMutex
	totalSentBytes  uint64
	totalRecvBytes  uint64
	connectionCount int32

	// 用于计算速率的历史数据
	sentBytesHistory []uint64
	recvBytesHistory []uint64
	timestamps       []time.Time
	historySize      int
}

// NewStats 创建一个新的统计实例
func NewStats(historySize int) *Stats {
	return &Stats{
		historySize:      historySize,
		sentBytesHistory: make([]uint64, 0, historySize),
		recvBytesHistory: make([]uint64, 0, historySize),
		timestamps:       make([]time.Time, 0, historySize),
	}
}

// AddSentBytes 增加已发送字节数
func (s *Stats) AddSentBytes(bytes uint64) {
	atomic.AddUint64(&s.totalSentBytes, bytes)
}

// AddRecvBytes 增加已接收字节数
func (s *Stats) AddRecvBytes(bytes uint64) {
	atomic.AddUint64(&s.totalRecvBytes, bytes)
}

// IncrementConnectionCount 增加连接计数
func (s *Stats) IncrementConnectionCount() {
	atomic.AddInt32(&s.connectionCount, 1)
}

// DecrementConnectionCount 减少连接计数
func (s *Stats) DecrementConnectionCount() {
	atomic.AddInt32(&s.connectionCount, -1)
}

// UpdateHistory 更新历史数据以计算速率
func (s *Stats) UpdateHistory() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// 获取当前总字节数
	currentSentBytes := atomic.LoadUint64(&s.totalSentBytes)
	currentRecvBytes := atomic.LoadUint64(&s.totalRecvBytes)
	currentTime := time.Now()

	// 添加到历史记录
	s.sentBytesHistory = append(s.sentBytesHistory, currentSentBytes)
	s.recvBytesHistory = append(s.recvBytesHistory, currentRecvBytes)
	s.timestamps = append(s.timestamps, currentTime)

	// 如果历史记录超过指定大小，移除最旧的记录
	if len(s.timestamps) > s.historySize {
		s.sentBytesHistory = s.sentBytesHistory[1:]
		s.recvBytesHistory = s.recvBytesHistory[1:]
		s.timestamps = s.timestamps[1:]
	}
}

// GetStats 获取当前统计数据
func (s *Stats) GetStats() map[string]interface{} {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	// 获取当前值
	totalSent := atomic.LoadUint64(&s.totalSentBytes)
	totalRecv := atomic.LoadUint64(&s.totalRecvBytes)
	connCount := atomic.LoadInt32(&s.connectionCount)

	// 计算发送和接收速率（字节/秒）
	var sendRate, recvRate float64

	historyLen := len(s.timestamps)
	if historyLen >= 2 {
		// 计算时间窗口（秒）
		timeWindow := s.timestamps[historyLen-1].Sub(s.timestamps[0]).Seconds()
		if timeWindow > 0 {
			// 计算在此时间窗口内传输的字节数
			sentBytes := s.sentBytesHistory[historyLen-1] - s.sentBytesHistory[0]
			recvBytes := s.recvBytesHistory[historyLen-1] - s.recvBytesHistory[0]

			// 计算速率
			sendRate = float64(sentBytes) / timeWindow
			recvRate = float64(recvBytes) / timeWindow
		}
	}

	return map[string]interface{}{
		"total_sent_bytes":    totalSent,
		"total_recv_bytes":    totalRecv,
		"connection_count":    connCount,
		"send_rate_bytes_sec": sendRate,
		"recv_rate_bytes_sec": recvRate,
	}
	//"total_sent_bytes":    fmt.Sprintf("%.2f K", totalSent/1024),
	//"total_recv_bytes":    fmt.Sprintf("%.2f K", totalRecv/1024),
	//"connection_count":    connCount,
	//"send_rate_bytes_sec": fmt.Sprintf("%.2f KBps", sendRate/1024),
	//"recv_rate_bytes_sec": fmt.Sprintf("%.2f KBps", recvRate/1024),

}

// 全局统计实例
var (
	ClientStats *Stats
	ServerStats *Stats
	once        sync.Once
)

// InitStats 初始化全局统计实例
func InitStats(historySize int) {
	once.Do(func() {
		ClientStats = NewStats(historySize)
		ServerStats = NewStats(historySize)

		// 启动一个goroutine定期更新历史数据
		go func() {
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()

			for range ticker.C {
				ClientStats.UpdateHistory()
				ServerStats.UpdateHistory()
			}
		}()
	})
}
