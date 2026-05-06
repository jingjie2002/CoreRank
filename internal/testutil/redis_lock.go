package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/redis/go-redis/v9"
)

const redisTestLockKey = "corerank:test:redis_lock"

var releaseRedisTestLockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`)

func AcquireRedisTestLock(ctx context.Context, client *redis.Client) (func(context.Context) error, error) {
	token := randomToken()

	for {
		acquired, err := client.SetNX(ctx, redisTestLockKey, token, time.Minute).Result()
		if err != nil {
			return nil, err
		}
		if acquired {
			return func(releaseCtx context.Context) error {
				return releaseRedisTestLockScript.Run(releaseCtx, client, []string{redisTestLockKey}, token).Err()
			}, nil
		}

		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func randomToken() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return time.Now().Format(time.RFC3339Nano)
}
