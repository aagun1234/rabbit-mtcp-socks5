package connection

var (
	OrderedRecvQueueSize    = 32        // OrderedRecvQueue channel cap
	RecvQueueSize           = 32        // RecvQueue channel cap
	OutboundRecvBuffer      = 32 * 1024 // 32K receive buffer for Outbound Connection
	OutboundBlockTimeoutSec = 3         // Wait the period and check exit signal
	PacketWaitTimeoutSec    = 7         // If block processor is waiting for a "hole", and no packet comes within this limit, the Connection will be closed
	DialTimeoutSec          = 6
)
