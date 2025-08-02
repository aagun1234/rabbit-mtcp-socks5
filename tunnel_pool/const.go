package tunnel_pool

var (
	DialTimeoutSec        = 6
	ReconnectDelaySec     = 5
	MaxRetries            = 3
	PingInterval          = 30
	ErrorWaitSec          = 3  // If a tunnel cannot be dialed, will wait for this period and retry infinitely
	TunnelBlockTimeoutSec = 8  // If a tunnel cannot send a block within the limit, will treat it a dead tunnel
	TunnelRecvTimeoutSec  = 40 // If a tunnel cannot recv timeout
	EmptyPoolDestroySec   = 60 // The pool will be destroyed(server side) if no tunnel dialed in
	SendQueueSize         = 64 // SendQueue channel cap
	RecvQueueSize         = 64 // RecvQueue channel cap
)
