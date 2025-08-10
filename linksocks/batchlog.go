package linksocks

import (
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// batchLogger handles batched logging of similar messages
type batchLogger struct {
	mu       sync.Mutex
	messages map[string]*batchMessage
	logger   zerolog.Logger
}

type batchMessage struct {
	count int
	total int
	timer *time.Timer
}

func newBatchLogger(logger zerolog.Logger) *batchLogger {
	return &batchLogger{
		messages: make(map[string]*batchMessage),
		logger:   logger,
	}
}

func (bl *batchLogger) log(key string, total int, logFn func(count, total int)) {
	bl.mu.Lock()
	defer bl.mu.Unlock()

	if total == 1 {
		logFn(1, total)
		return
	}

	if msg, exists := bl.messages[key]; exists {
		msg.count++
		if msg.timer != nil {
			msg.timer.Stop()
		}
	} else {
		bl.messages[key] = &batchMessage{count: 1, total: total}
	}

	msg := bl.messages[key]
	msg.timer = time.AfterFunc(500*time.Millisecond, func() {
		bl.mu.Lock()
		defer bl.mu.Unlock()
		if m, ok := bl.messages[key]; ok {
			logFn(m.count, m.total)
			delete(bl.messages, key)
		}
	})
}
