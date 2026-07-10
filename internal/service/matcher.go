package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jingjie2002/CoreRank/internal/repository"
)

type ScoreBucket struct {
	Name            string
	BaseMinScore    int64
	BaseMaxScore    int64
	CurrentMinScore int64
	CurrentMaxScore int64
	EmptyMatchCount int
	ExpandThreshold int
	ExpandStep      int64
}

type MatchWorker struct {
	playerRepo          *repository.PlayerRepository
	matchService        *MatchService
	matchInterval       time.Duration
	playersPerMatch     int
	buckets             []*ScoreBucket
	modeBuckets         map[string][]*ScoreBucket
	matchedTotal        atomic.Int64
	lastAssignmentSweep time.Time
}

const timeoutSweepLimit = 100
const assignmentSweepLimit = 100
const assignmentSweepInterval = 10 * time.Second

func NewMatchWorker(playerRepo *repository.PlayerRepository) *MatchWorker {
	buckets := []*ScoreBucket{
		newScoreBucket("bronze", 0, 1000),
		newScoreBucket("silver", 1001, 2000),
		newScoreBucket("gold", 2001, 3000),
		newScoreBucket("platinum", 3001, 4000),
		newScoreBucket("diamond", 4001, 5000),
		newScoreBucket("high_mmr", 5001, maxMMRScore),
	}
	return &MatchWorker{
		playerRepo:      playerRepo,
		matchInterval:   100 * time.Millisecond,
		playersPerMatch: matchPlayersPerRoom,
		buckets:         buckets,
		modeBuckets:     make(map[string][]*ScoreBucket),
	}
}

func newScoreBucket(name string, minScore, maxScore int64) *ScoreBucket {
	return &ScoreBucket{
		Name: name, BaseMinScore: minScore, BaseMaxScore: maxScore,
		CurrentMinScore: minScore, CurrentMaxScore: maxScore,
		ExpandThreshold: 3, ExpandStep: 200,
	}
}

func (w *MatchWorker) SetMatchService(matchService *MatchService) {
	w.matchService = matchService
}

func (w *MatchWorker) Start(ctx context.Context) {
	matchTicker := time.NewTicker(w.matchInterval)
	statsTicker := time.NewTicker(5 * time.Second)
	go func() {
		defer matchTicker.Stop()
		defer statsTicker.Stop()
		var previousTotal int64
		previousAt := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-matchTicker.C:
				w.sweepExpiredTickets(ctx)
				w.sweepExpiredAssignments(ctx)
				w.scanAllBuckets(ctx)
			case <-statsTicker.C:
				current := w.matchedTotal.Load()
				now := time.Now()
				rate := float64(current-previousTotal) / now.Sub(previousAt).Seconds()
				fmt.Printf("[Matcher] matched_total=%d recent_matches_per_second=%.2f\n", current, rate)
				previousTotal = current
				previousAt = now
			}
		}
	}()
}

func (w *MatchWorker) sweepExpiredAssignments(ctx context.Context) {
	if w.matchService == nil {
		return
	}
	now := time.Now()
	if !w.lastAssignmentSweep.IsZero() && now.Sub(w.lastAssignmentSweep) < assignmentSweepInterval {
		return
	}
	w.lastAssignmentSweep = now
	if _, err := w.matchService.ReleaseExpiredRoomAssignments(ctx, now, assignmentSweepLimit); err != nil {
		fmt.Printf("[Matcher] assignment lease sweep failed: %v\n", err)
	}
}

func (w *MatchWorker) sweepExpiredTickets(ctx context.Context) {
	if w.matchService == nil {
		return
	}
	if _, err := w.matchService.TimeoutExpiredTickets(ctx, time.Now(), timeoutSweepLimit); err != nil {
		fmt.Printf("[Matcher] timeout sweep failed: %v\n", err)
	}
}

func (w *MatchWorker) scanAllBuckets(ctx context.Context) {
	modes, err := w.playerRepo.ListQueuedMatchModes(ctx)
	if err != nil {
		fmt.Printf("[Matcher] list queued match modes failed: %v\n", err)
		return
	}
	for _, mode := range modes {
		for _, bucket := range w.bucketsForMode(mode) {
			w.matchInBucket(ctx, mode, bucket)
		}
	}
}

func (w *MatchWorker) bucketsForMode(matchMode string) []*ScoreBucket {
	if buckets, ok := w.modeBuckets[matchMode]; ok {
		return buckets
	}
	buckets := make([]*ScoreBucket, 0, len(w.buckets))
	for _, template := range w.buckets {
		bucket := *template
		buckets = append(buckets, &bucket)
	}
	w.modeBuckets[matchMode] = buckets
	return buckets
}

func (w *MatchWorker) matchInBucket(ctx context.Context, matchMode string, bucket *ScoreBucket) {
	players, err := w.playerRepo.SearchAndPickTicketPlayers(
		ctx, matchMode, bucket.CurrentMinScore, bucket.CurrentMaxScore, w.playersPerMatch,
	)
	if err != nil {
		fmt.Printf("[Matcher] mode=%s bucket=%s scan failed: %v\n", matchMode, bucket.Name, err)
		return
	}
	if len(players) < w.playersPerMatch {
		bucket.EmptyMatchCount++
		if bucket.EmptyMatchCount >= bucket.ExpandThreshold {
			w.expandBucketRange(bucket)
		}
		return
	}

	if w.matchService == nil {
		if err := w.playerRepo.RequeueMatchTicketPlayers(ctx, players); err != nil {
			fmt.Printf("[Matcher] mode=%s requeue failed: %v\n", matchMode, err)
		}
		return
	}
	result, err := w.matchService.CompletePickedPlayers(ctx, players, matchMode)
	if err != nil {
		fmt.Printf("[Matcher] mode=%s completion failed: %v\n", matchMode, err)
		return
	}
	if result == nil {
		return
	}

	bucket.EmptyMatchCount = 0
	bucket.CurrentMinScore = bucket.BaseMinScore
	bucket.CurrentMaxScore = bucket.BaseMaxScore
	w.matchedTotal.Add(1)
	if len(w.modeBuckets) > 256 {
		w.pruneInactiveModes(ctx, modesWithout(matchMode))
	}
	fmt.Printf("[Matcher] mode=%s match_id=%s room_id=%s players=%v\n", matchMode, result.MatchID, result.RoomID, players)
}

func (w *MatchWorker) expandBucketRange(bucket *ScoreBucket) {
	bucket.CurrentMinScore -= bucket.ExpandStep
	if bucket.CurrentMinScore < 0 {
		bucket.CurrentMinScore = 0
	}
	bucket.CurrentMaxScore += bucket.ExpandStep
	if bucket.CurrentMaxScore > maxMMRScore {
		bucket.CurrentMaxScore = maxMMRScore
	}
	bucket.EmptyMatchCount = 0
}

func (w *MatchWorker) pruneInactiveModes(ctx context.Context, keep map[string]struct{}) {
	active, err := w.playerRepo.ListQueuedMatchModes(ctx)
	if err != nil {
		return
	}
	for _, mode := range active {
		keep[mode] = struct{}{}
	}
	for mode := range w.modeBuckets {
		if _, ok := keep[mode]; !ok {
			delete(w.modeBuckets, mode)
		}
	}
}

func modesWithout(mode string) map[string]struct{} {
	return map[string]struct{}{mode: {}}
}
