package graph

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

const (
	episodeCompressQueueSize    = 256
	episodeCompressScanInterval = 5 * time.Minute
	episodeCompressScanBatch    = 100
)

// EpisodeCompressQueue serialises episode pyramid compression.
// At most one episode is compressed at a time. A fixed-size channel buffers
// pending IDs; when it overflows, needsScan is set so a periodic scan can
// pick up any missed episodes from the database.
type EpisodeCompressQueue struct {
	db         *DB
	compressor Compressor
	logger     *slog.Logger
	queue      chan string
	needsScan  atomic.Bool
}

// NewEpisodeCompressQueue creates a queue ready to be started.
// Sets needsScan=true on creation so any pre-existing uncompressed episodes
// are picked up on the first scan tick.
func NewEpisodeCompressQueue(db *DB, compressor Compressor, logger *slog.Logger) *EpisodeCompressQueue {
	q := &EpisodeCompressQueue{
		db:         db,
		compressor: compressor,
		logger:     logger,
		queue:      make(chan string, episodeCompressQueueSize),
	}
	q.needsScan.Store(true)
	return q
}

// Enqueue adds an episode ID to the compression queue without blocking.
// If the queue is full the ID is dropped and needsScan is set so the
// background scan will catch it.
func (q *EpisodeCompressQueue) Enqueue(episodeID string) {
	select {
	case q.queue <- episodeID:
	default:
		q.needsScan.Store(true)
		q.logger.Warn("episode compress queue full, will scan later", "episode_id", episodeID)
	}
}

// Start runs the compression worker until ctx is cancelled.
// Must be called exactly once (typically as a goroutine).
func (q *EpisodeCompressQueue) Start(ctx context.Context) {
	ticker := time.NewTicker(episodeCompressScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case id := <-q.queue:
			q.compress(id)
		case <-ticker.C:
			if q.needsScan.CompareAndSwap(true, false) {
				q.scan(ctx)
			}
		}
	}
}

func (q *EpisodeCompressQueue) compress(episodeID string) {
	ep, err := q.db.GetEpisode(episodeID)
	if err != nil {
		q.logger.Warn("episode compress: episode not found", "episode_id", episodeID, "err", err)
		return
	}
	q.db.generateCompressedSummaries(*ep, q.compressor)
}

func (q *EpisodeCompressQueue) scan(ctx context.Context) {
	ids, err := q.db.GetEpisodesWithoutSummaries(episodeCompressScanBatch)
	if err != nil {
		q.logger.Warn("episode compress: scan failed", "err", err)
		q.needsScan.Store(true)
		return
	}
	if len(ids) == 0 {
		return
	}
	q.logger.Info("episode compress: backfilling missing summaries", "count", len(ids))
	for _, id := range ids {
		select {
		case <-ctx.Done():
			return
		case q.queue <- id:
		default:
			// Queue filled up again; bail and let the next tick continue.
			q.needsScan.Store(true)
			return
		}
	}
	// If we got a full batch there may be more rows waiting.
	if len(ids) == episodeCompressScanBatch {
		q.needsScan.Store(true)
	}
}
