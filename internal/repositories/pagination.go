package repositories

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TimeCursor is the last row of the previous page, for lists sorted newest first.
// The ID breaks ties: two rows can share a timestamp, and a cursor of created_at
// alone would skip or repeat them at a page boundary.
type TimeCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// newestFirst sorts q by (created_at DESC, id DESC) and starts after the cursor, if
// any. Both columns sort the same way, so the row comparison matches the sort order
// and one index can serve both. It never uses OFFSET, which reads and discards every
// skipped row and shifts when rows are inserted between pages.
func newestFirst(q *gorm.DB, after *TimeCursor, limit int) *gorm.DB {
	if after != nil {
		q = q.Where("(created_at, id) < (?, ?)", after.CreatedAt, after.ID)
	}
	return q.Order("created_at DESC, id DESC").Limit(limit)
}
