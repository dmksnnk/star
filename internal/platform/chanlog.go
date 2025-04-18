package platform

// ChanLog sends logs to a channel.
type ChanLog struct {
	logs chan []byte
}

// NewChanLog creates a new ChanLog.
func NewChanLog() *ChanLog {
	return &ChanLog{
		logs: make(chan []byte, 1),
	}
}

// Logs returns a channel that receives logs.
func (cl *ChanLog) Logs() <-chan []byte {
	return cl.logs
}

// Write sends logs to the channel.
// If the channel is full, it drops the log.
func (cl *ChanLog) Write(p []byte) (n int, err error) {
	n = len(p)
	if n == 0 {
		return
	}

	select {
	case cl.logs <- p:
	default:
	}
	return
}
