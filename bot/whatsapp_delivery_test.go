package bot

import (
	"fmt"
	"testing"
)

func TestDeliveryLogMarkProcessed(t *testing.T) {
	log := NewDeliveryLog()
	if !log.MarkProcessed("msg-1") {
		t.Fatal("first MarkProcessed() = false")
	}
	if log.MarkProcessed("msg-1") {
		t.Fatal("duplicate MarkProcessed() = true")
	}
}

func TestDeliveryLogRecordAttempt(t *testing.T) {
	log := NewDeliveryLog()
	log.RecordAttempt("msg-1", "628123", "failed", fmt.Errorf("boom"))
	log.RecordAttempt("msg-1", "628123", "sent", nil)
	attempts := log.Attempts()
	if len(attempts) != 2 {
		t.Fatalf("attempt count = %d", len(attempts))
	}
	if attempts[0].ResponseExcerpt != "boom" {
		t.Fatalf("excerpt = %q", attempts[0].ResponseExcerpt)
	}
}
