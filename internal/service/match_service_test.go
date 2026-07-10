package service

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jingjie2002/CoreRank/internal/repository"
	"github.com/jingjie2002/CoreRank/internal/testutil"

	"github.com/redis/go-redis/v9"
)

func newTestMatchService(t *testing.T) (*MatchService, func()) {
	t.Helper()

	matchService, _, cleanup := newTestMatchServiceComponents(t)
	return matchService, cleanup
}

func newTestMatchServiceWithRoomServers(t *testing.T) (*MatchService, *repository.RoomServerRepository, func()) {
	t.Helper()

	matchService, roomServerRepo, cleanup := newTestMatchServiceComponents(t)
	matchService.SetRoomServerRepository(roomServerRepo)
	return matchService, roomServerRepo, cleanup
}

func newTestMatchServiceComponents(t *testing.T) (*MatchService, *repository.RoomServerRepository, func()) {
	t.Helper()

	addr := os.Getenv("CORERANK_TEST_REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}

	client := redis.NewClient(&redis.Options{Addr: addr, DB: 0})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("redis is unavailable at %s: %v", addr, err)
	}

	releaseLock := acquireRedisTestLock(t, client)
	if err := cleanMatchServiceTestKeys(ctx, client); err != nil {
		releaseLock()
		_ = client.Close()
		t.Fatalf("clean redis keys: %v", err)
	}

	repo := repository.NewPlayerRepository(client)
	roomServerRepo := repository.NewRoomServerRepository(client)
	cleanup := func() {
		_ = cleanMatchServiceTestKeys(context.Background(), client)
		releaseLock()
		_ = client.Close()
	}
	return NewMatchService(repo), roomServerRepo, cleanup
}

func acquireRedisTestLock(t *testing.T, client *redis.Client) func() {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	release, err := testutil.AcquireRedisTestLock(ctx, client)
	if err != nil {
		_ = client.Close()
		t.Fatalf("acquire redis test lock: %v", err)
	}

	return func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		_ = release(releaseCtx)
	}
}

func cleanMatchServiceTestKeys(ctx context.Context, client *redis.Client) error {
	if err := client.Del(ctx, repository.MatchPoolKey, repository.MatchTicketPoolKey, repository.MatchTicketModesKey, repository.MatchTicketExpiryKey, repository.GlobalRankKey).Err(); err != nil {
		return err
	}

	for _, pattern := range []string{"match:*", "{match:*}", "server:*", "room:assignment:*", "{rank:*}"} {
		var cursor uint64
		for {
			keys, nextCursor, err := client.Scan(ctx, cursor, pattern, 100).Result()
			if err != nil {
				return err
			}
			if len(keys) > 0 {
				if err := client.Del(ctx, keys...).Err(); err != nil {
					return err
				}
			}
			cursor = nextCursor
			if cursor == 0 {
				break
			}
		}
	}
	return nil
}

type fixedRoomAllocator struct {
	roomID string
}

func (a fixedRoomAllocator) AllocateRoom(_ context.Context, req RoomAllocationRequest) (repository.RoomAssignment, error) {
	return repository.RoomAssignment{
		MatchID:   req.MatchID,
		RoomID:    a.roomID,
		MatchMode: req.MatchMode,
		PlayerIDs: append([]string(nil), req.PlayerIDs...),
		Status:    repository.RoomAssignmentStatusAssigned,
		CreatedAt: time.Now().UnixMilli(),
	}, nil
}

func (fixedRoomAllocator) ReleaseRoom(context.Context, repository.RoomAssignment) error {
	return nil
}

func (fixedRoomAllocator) SaveAssignment(context.Context, repository.RoomAssignment) error {
	return nil
}

type failingRoomAllocator struct {
	err error
}

func (a failingRoomAllocator) AllocateRoom(context.Context, RoomAllocationRequest) (repository.RoomAssignment, error) {
	return repository.RoomAssignment{}, a.err
}

func (failingRoomAllocator) ReleaseRoom(context.Context, repository.RoomAssignment) error {
	return nil
}

func (failingRoomAllocator) SaveAssignment(context.Context, repository.RoomAssignment) error {
	return nil
}

func TestMatchTicketsCreateMatchedResult(t *testing.T) {
	matchService, cleanup := newTestMatchService(t)
	defer cleanup()

	ctx := context.Background()
	first, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "p1",
		MMRScore: 1200,
		MaxWait:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create first ticket: %v", err)
	}
	if first.Status != repository.MatchStatusQueued {
		t.Fatalf("first ticket should wait, got %s", first.Status)
	}

	second, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "p2",
		MMRScore: 1210,
		MaxWait:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create second ticket: %v", err)
	}
	if second.Status != repository.MatchStatusMatched {
		t.Fatalf("second ticket should be matched, got %s", second.Status)
	}
	if second.MatchID == "" || second.RoomID == "" {
		t.Fatalf("matched ticket should include match and room ids: %#v", second)
	}

	refreshedFirst, err := matchService.GetTicket(ctx, first.TicketID)
	if err != nil {
		t.Fatalf("get first ticket: %v", err)
	}
	if refreshedFirst.Status != repository.MatchStatusMatched || refreshedFirst.MatchID != second.MatchID {
		t.Fatalf("first ticket should share matched result, got %#v", refreshedFirst)
	}

	result, err := matchService.GetResult(ctx, second.MatchID)
	if err != nil {
		t.Fatalf("get match result: %v", err)
	}
	if result.RoomID != second.RoomID {
		t.Fatalf("result room mismatch: %#v", result)
	}
	if !reflect.DeepEqual(result.PlayerIDs, []string{"p1", "p2"}) {
		t.Fatalf("unexpected matched players: %#v", result.PlayerIDs)
	}
}

func TestMatchTicketsNeverCrossMatchModes(t *testing.T) {
	matchService, cleanup := newTestMatchService(t)
	defer cleanup()

	ctx := context.Background()
	defaultFirst, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "default-p1", MMRScore: 1200, MatchMode: "default", MaxWait: time.Minute,
	})
	if err != nil {
		t.Fatalf("create default ticket: %v", err)
	}
	duelFirst, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "duel-p1", MMRScore: 1205, MatchMode: "duel", MaxWait: time.Minute,
	})
	if err != nil {
		t.Fatalf("create duel ticket: %v", err)
	}
	if defaultFirst.Status != repository.MatchStatusQueued || duelFirst.Status != repository.MatchStatusQueued {
		t.Fatalf("different modes must not match each other: default=%#v duel=%#v", defaultFirst, duelFirst)
	}

	defaultSecond, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "default-p2", MMRScore: 1210, MatchMode: "default", MaxWait: time.Minute,
	})
	if err != nil {
		t.Fatalf("create second default ticket: %v", err)
	}
	if defaultSecond.Status != repository.MatchStatusMatched {
		t.Fatalf("default mode should match within its queue, got %#v", defaultSecond)
	}
	defaultResult, err := matchService.GetResult(ctx, defaultSecond.MatchID)
	if err != nil {
		t.Fatalf("get default result: %v", err)
	}
	if defaultResult.MatchMode != "default" || !reflect.DeepEqual(defaultResult.PlayerIDs, []string{"default-p1", "default-p2"}) {
		t.Fatalf("unexpected default result: %#v", defaultResult)
	}

	duelQueued, err := matchService.GetTicket(ctx, duelFirst.TicketID)
	if err != nil {
		t.Fatalf("get queued duel ticket: %v", err)
	}
	if duelQueued.Status != repository.MatchStatusQueued {
		t.Fatalf("duel ticket must remain queued, got %#v", duelQueued)
	}

	duelSecond, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "duel-p2", MMRScore: 1215, MatchMode: "duel", MaxWait: time.Minute,
	})
	if err != nil {
		t.Fatalf("create second duel ticket: %v", err)
	}
	duelResult, err := matchService.GetResult(ctx, duelSecond.MatchID)
	if err != nil {
		t.Fatalf("get duel result: %v", err)
	}
	if duelResult.MatchMode != "duel" || !reflect.DeepEqual(duelResult.PlayerIDs, []string{"duel-p1", "duel-p2"}) {
		t.Fatalf("unexpected duel result: %#v", duelResult)
	}
}

func TestMatchSettlementValidatesPlayersIsIdempotentAndReleasesCapacity(t *testing.T) {
	matchService, roomServerRepo, cleanup := newTestMatchServiceWithRoomServers(t)
	defer cleanup()

	ctx := context.Background()
	_, err := matchService.RegisterGameServer(ctx, repository.GameServer{
		ServerID: "settlement-room", Addr: "127.0.0.1:7501", MatchMode: "duel",
		Capacity: 2, Status: repository.GameServerStatusActive,
	})
	if err != nil {
		t.Fatalf("register room server: %v", err)
	}
	_, err = matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "settle-p1", MMRScore: 1500, MatchMode: "duel", MaxWait: time.Minute,
	})
	if err != nil {
		t.Fatalf("create first ticket: %v", err)
	}
	second, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "settle-p2", MMRScore: 1510, MatchMode: "duel", MaxWait: time.Minute,
	})
	if err != nil {
		t.Fatalf("create second ticket: %v", err)
	}
	if second.Status != repository.MatchStatusMatched {
		t.Fatalf("match should be ready for settlement, got %#v", second)
	}

	rankService := NewRankService(matchService.playerRepo)
	_, err = rankService.SettleMatch(ctx, second.MatchID, "global", []repository.SettlementScore{
		{PlayerID: "settle-p1", Score: 100},
		{PlayerID: "outsider", Score: 9999},
	})
	if !errors.Is(err, ErrInvalidSettlementPlayers) {
		t.Fatalf("outsider settlement must be rejected, got %v", err)
	}
	if _, err := matchService.playerRepo.GetPlayerScore(ctx, "outsider"); !errors.Is(err, redis.Nil) {
		t.Fatalf("outsider score must not be written, got %v", err)
	}

	scores := []repository.SettlementScore{
		{PlayerID: "settle-p1", Score: 1200},
		{PlayerID: "settle-p2", Score: 1100},
	}
	settled, err := rankService.SettleMatch(ctx, second.MatchID, "global", scores)
	if err != nil {
		t.Fatalf("settle match: %v", err)
	}
	if settled.Idempotent {
		t.Fatal("first settlement must not be reported as idempotent replay")
	}
	server, err := roomServerRepo.GetGameServer(ctx, "settlement-room")
	if err != nil {
		t.Fatalf("get released room server: %v", err)
	}
	if server.CurrentLoad != 0 {
		t.Fatalf("settlement must release reserved capacity, got %#v", server)
	}

	replayed, err := rankService.SettleMatch(ctx, second.MatchID, "global", []repository.SettlementScore{
		{PlayerID: "settle-p2", Score: 1100},
		{PlayerID: "settle-p1", Score: 1200},
	})
	if err != nil {
		t.Fatalf("replay identical settlement: %v", err)
	}
	if !replayed.Idempotent {
		t.Fatal("identical settlement replay must be idempotent")
	}

	_, err = rankService.SettleMatch(ctx, second.MatchID, "global", []repository.SettlementScore{
		{PlayerID: "settle-p1", Score: 1},
		{PlayerID: "settle-p2", Score: 2},
	})
	if !errors.Is(err, repository.ErrSettlementConflict) {
		t.Fatalf("different settlement replay must conflict, got %v", err)
	}
	score, err := matchService.playerRepo.GetPlayerScore(ctx, "settle-p1")
	if err != nil || score != 1200 {
		t.Fatalf("conflicting replay must not alter scores, score=%v err=%v", score, err)
	}
}

func TestMatchServiceUsesRoomAllocator(t *testing.T) {
	matchService, cleanup := newTestMatchService(t)
	defer cleanup()
	matchService.SetRoomAllocator(fixedRoomAllocator{roomID: "room_fixed"})

	ctx := context.Background()
	_, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "room-p1",
		MMRScore: 1800,
		MaxWait:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create first ticket: %v", err)
	}
	second, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "room-p2",
		MMRScore: 1810,
		MaxWait:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create second ticket: %v", err)
	}
	if second.RoomID != "room_fixed" {
		t.Fatalf("expected fixed room id, got %#v", second)
	}

	result, err := matchService.GetResult(ctx, second.MatchID)
	if err != nil {
		t.Fatalf("get match result: %v", err)
	}
	if result.RoomID != "room_fixed" {
		t.Fatalf("result should use fixed room id, got %#v", result)
	}
}

func TestMatchServiceRequeuesTicketsWhenRoomAllocationFails(t *testing.T) {
	matchService, cleanup := newTestMatchService(t)
	defer cleanup()
	matchService.SetRoomAllocator(failingRoomAllocator{err: repository.ErrNoAvailableRoomServer})

	ctx := context.Background()
	first, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "room-fail-p1",
		MMRScore: 1200,
		MaxWait:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create first ticket: %v", err)
	}
	if first.Status != repository.MatchStatusQueued {
		t.Fatalf("first ticket should wait, got %s", first.Status)
	}

	second, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "room-fail-p2",
		MMRScore: 1210,
		MaxWait:  time.Minute,
	})
	if err != nil {
		t.Fatalf("room allocation failure should keep ticket queued instead of failing request: %v", err)
	}
	if second.Status != repository.MatchStatusQueued || second.MatchID != "" {
		t.Fatalf("second ticket should stay queued when no room server is available, got %#v", second)
	}

	queued, err := matchService.playerRepo.CountQueuedMatchTickets(ctx, defaultMatchMode)
	if err != nil {
		t.Fatalf("count queued tickets: %v", err)
	}
	if queued != 2 {
		t.Fatalf("expected both players to be requeued, got %d", queued)
	}

	matchService.SetRoomAllocator(fixedRoomAllocator{roomID: "room_recovered"})
	result, err := matchService.TryCompleteMatch(ctx, 1205, defaultMatchMode)
	if err != nil {
		t.Fatalf("complete requeued tickets: %v", err)
	}
	if result == nil || result.RoomID != "room_recovered" {
		t.Fatalf("expected requeued tickets to complete after allocator recovers, got %#v", result)
	}

	queued, err = matchService.playerRepo.CountQueuedMatchTickets(ctx, defaultMatchMode)
	if err != nil {
		t.Fatalf("count queued tickets after recovery: %v", err)
	}
	if queued != 0 {
		t.Fatalf("expected ticket pool to be empty after recovery, got %d", queued)
	}
}

func TestMatchServiceRequeuesTicketsWhenRoomServerCapacityIsInsufficient(t *testing.T) {
	matchService, roomServerRepo, cleanup := newTestMatchServiceWithRoomServers(t)
	defer cleanup()

	ctx := context.Background()
	_, err := matchService.RegisterGameServer(ctx, repository.GameServer{
		ServerID:        "room-capacity-1",
		Addr:            "127.0.0.1:7401",
		MatchMode:       "duel",
		Capacity:        1,
		Status:          repository.GameServerStatusActive,
		LastHeartbeatAt: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("register capacity-limited room server: %v", err)
	}

	first, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID:  "capacity-p1",
		MMRScore:  1200,
		MatchMode: "duel",
		MaxWait:   time.Minute,
	})
	if err != nil {
		t.Fatalf("create first ticket: %v", err)
	}
	if first.Status != repository.MatchStatusQueued {
		t.Fatalf("first ticket should wait, got %#v", first)
	}

	second, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID:  "capacity-p2",
		MMRScore:  1210,
		MatchMode: "duel",
		MaxWait:   time.Minute,
	})
	if err != nil {
		t.Fatalf("capacity failure should keep ticket queued instead of failing request: %v", err)
	}
	if second.Status != repository.MatchStatusQueued || second.MatchID != "" || second.RoomID != "" {
		t.Fatalf("second ticket should stay queued without a partial match result, got %#v", second)
	}

	refreshedFirst, err := matchService.GetTicket(ctx, first.TicketID)
	if err != nil {
		t.Fatalf("get first ticket after capacity failure: %v", err)
	}
	if refreshedFirst.Status != repository.MatchStatusQueued || refreshedFirst.MatchID != "" || refreshedFirst.RoomID != "" {
		t.Fatalf("first ticket should stay queued without a partial match result, got %#v", refreshedFirst)
	}

	queued, err := matchService.playerRepo.CountQueuedMatchTickets(ctx, "duel")
	if err != nil {
		t.Fatalf("count queued tickets: %v", err)
	}
	if queued != 2 {
		t.Fatalf("expected both tickets to be requeued, got %d", queued)
	}

	server, err := roomServerRepo.GetGameServer(ctx, "room-capacity-1")
	if err != nil {
		t.Fatalf("get capacity-limited room server: %v", err)
	}
	if server.CurrentLoad != 0 {
		t.Fatalf("capacity failure should not reserve room server load, got %#v", server)
	}

	_, err = matchService.RegisterGameServer(ctx, repository.GameServer{
		ServerID:        "room-capacity-1",
		Addr:            "127.0.0.1:7401",
		MatchMode:       "duel",
		Capacity:        4,
		Status:          repository.GameServerStatusActive,
		LastHeartbeatAt: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("register recovered room server: %v", err)
	}
	result, err := matchService.TryCompleteMatch(ctx, 1205, "duel")
	if err != nil {
		t.Fatalf("complete requeued tickets after room server recovers: %v", err)
	}
	if result == nil || result.ServerID != "room-capacity-1" || result.ServerAddr != "127.0.0.1:7401" {
		t.Fatalf("expected recovered room server assignment, got %#v", result)
	}

	queued, err = matchService.playerRepo.CountQueuedMatchTickets(ctx, "duel")
	if err != nil {
		t.Fatalf("count queued tickets after recovery: %v", err)
	}
	if queued != 0 {
		t.Fatalf("expected queue to drain after recovery, got %d", queued)
	}
}

func TestMatchTicketCanBeCancelled(t *testing.T) {
	matchService, cleanup := newTestMatchService(t)
	defer cleanup()

	ctx := context.Background()
	ticket, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "cancel-me",
		MMRScore: 1500,
		MaxWait:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	cancelled, err := matchService.CancelTicket(ctx, ticket.TicketID)
	if err != nil {
		t.Fatalf("cancel ticket: %v", err)
	}
	if cancelled.Status != repository.MatchStatusCancelled {
		t.Fatalf("ticket should be cancelled, got %s", cancelled.Status)
	}

	_, err = matchService.CancelTicket(ctx, ticket.TicketID)
	if !errors.Is(err, repository.ErrTicketNotQueued) {
		t.Fatalf("second cancellation should fail as not queued, got %v", err)
	}
}

func TestMatchTicketCanTimeout(t *testing.T) {
	matchService, cleanup := newTestMatchService(t)
	defer cleanup()

	ctx := context.Background()
	ticket, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "timeout-me",
		MMRScore: 1500,
		MaxWait:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	timedOut, err := matchService.TimeoutExpiredTickets(ctx, time.UnixMilli(ticket.ExpiresAt+1), 10)
	if err != nil {
		t.Fatalf("timeout expired tickets: %v", err)
	}
	if len(timedOut) != 1 || timedOut[0].TicketID != ticket.TicketID {
		t.Fatalf("unexpected timed out tickets: %#v", timedOut)
	}
	if timedOut[0].Status != repository.MatchStatusTimeout {
		t.Fatalf("ticket should timeout, got %#v", timedOut[0])
	}

	saved, err := matchService.GetTicket(ctx, ticket.TicketID)
	if err != nil {
		t.Fatalf("get timed out ticket: %v", err)
	}
	if saved.Status != repository.MatchStatusTimeout {
		t.Fatalf("saved ticket should be timeout, got %#v", saved)
	}

	_, err = matchService.CancelTicket(ctx, ticket.TicketID)
	if !errors.Is(err, repository.ErrTicketNotQueued) {
		t.Fatalf("cancel timed out ticket should fail as not queued, got %v", err)
	}

	retry, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "timeout-me",
		MMRScore: 1510,
		MaxWait:  time.Minute,
	})
	if err != nil {
		t.Fatalf("timed out player should be able to requeue: %v", err)
	}
	if retry.Status != repository.MatchStatusQueued {
		t.Fatalf("retry ticket should be queued, got %#v", retry)
	}
}

func TestCancelAndTimeoutRaceLeavesOneTerminalState(t *testing.T) {
	matchService, cleanup := newTestMatchService(t)
	defer cleanup()

	ctx := context.Background()
	ticket, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "race-terminal", MMRScore: 1500, MatchMode: "duel", MaxWait: time.Minute,
	})
	if err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, _ = matchService.CancelTicket(ctx, ticket.TicketID)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, _ = matchService.TimeoutExpiredTickets(ctx, time.UnixMilli(ticket.ExpiresAt+1), 10)
	}()
	close(start)
	wg.Wait()

	finalTicket, err := matchService.GetTicket(ctx, ticket.TicketID)
	if err != nil {
		t.Fatalf("get terminal ticket: %v", err)
	}
	if finalTicket.Status != repository.MatchStatusCancelled && finalTicket.Status != repository.MatchStatusTimeout {
		t.Fatalf("expected exactly one terminal state, got %#v", finalTicket)
	}
	if _, err := matchService.playerRepo.GetPlayerTicketID(ctx, ticket.PlayerID); !errors.Is(err, redis.Nil) {
		t.Fatalf("terminal transition must remove player mapping, got %v", err)
	}
	queued, err := matchService.playerRepo.CountQueuedMatchTickets(ctx, "duel")
	if err != nil || queued != 0 {
		t.Fatalf("terminal transition must drain queue, queued=%d err=%v", queued, err)
	}
}

func TestDuplicateQueuedTicketIsRejected(t *testing.T) {
	matchService, cleanup := newTestMatchService(t)
	defer cleanup()

	ctx := context.Background()
	_, err := matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "dupe",
		MMRScore: 1500,
		MaxWait:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create first ticket: %v", err)
	}

	_, err = matchService.CreateTicket(ctx, CreateMatchTicketRequest{
		PlayerID: "dupe",
		MMRScore: 1500,
		MaxWait:  time.Minute,
	})
	if !errors.Is(err, repository.ErrPlayerAlreadyQueued) {
		t.Fatalf("duplicate ticket should be rejected, got %v", err)
	}
}
