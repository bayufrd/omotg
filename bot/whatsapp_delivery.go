package bot

import (
	"fmt"
	"sync"
	"time"
)

type DeliveryAttempt struct {
	MessageID        string
	TargetNumber     string
	Status           string
	ResponseExcerpt  string
	AttemptedAt      time.Time
}

type DeliveryLog struct {
	mu                sync.Mutex
	processedMessages map[string]time.Time
	attempts          []DeliveryAttempt
}

func NewDeliveryLog() *DeliveryLog {
	return &DeliveryLog{
		processedMessages: make(map[string]time.Time),
		attempts:          make([]DeliveryAttempt, 0, 32),
	}
}

func (d *DeliveryLog) MarkProcessed(messageID string) bool {
	if messageID == "" {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.processedMessages[messageID]; exists {
		return false
	}
	d.processedMessages[messageID] = time.Now()
	return true
}

func (d *DeliveryLog) RecordAttempt(messageID, targetNumber, status string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	excerpt := ""
	if err != nil {
		excerpt = err.Error()
	}
	d.attempts = append(d.attempts, DeliveryAttempt{
		MessageID:       messageID,
		TargetNumber:    targetNumber,
		Status:          status,
		ResponseExcerpt: excerpt,
		AttemptedAt:     time.Now(),
	})
}

func (d *DeliveryLog) Attempts() []DeliveryAttempt {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]DeliveryAttempt, len(d.attempts))
	copy(out, d.attempts)
	return out
}

func (d *DeliveryLog) Summary() string {
	attempts := d.Attempts()
	return fmt.Sprintf("processed=%d attempts=%d", len(d.processedMessages), len(attempts))
}
