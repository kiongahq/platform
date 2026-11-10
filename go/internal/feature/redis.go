package feature

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStore is the production online store for the platform key convention:
// one JSON document per (feature service, entity) under
// kionga:features:<service>:<entityKey>. Materialization jobs write through
// the feature-gateway PUT endpoint; lookups are single Redis GETs, which keeps
// the hot path inside the <5ms P99 budget.
type RedisStore struct {
	client *redis.Client
	ttl    time.Duration
}

// NewRedisStore connects using a redis:// URL. A zero ttl means keys do not
// expire (materialization controls freshness).
func NewRedisStore(redisURL string, ttl time.Duration) (*RedisStore, error) {
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	return &RedisStore{client: redis.NewClient(options), ttl: ttl}, nil
}

func (s *RedisStore) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

func key(service, entityKey string) string {
	return "kionga:features:" + service + ":" + entityKey
}

func (s *RedisStore) Get(service, entityKey string) (map[string]any, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := s.client.Get(ctx, key(service, entityKey)).Bytes()
	if err != nil {
		return nil, false
	}
	values := map[string]any{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, false
	}
	return values, true
}

func (s *RedisStore) Put(service, entityKey string, values map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := json.Marshal(values)
	if err != nil {
		return
	}
	_ = s.client.Set(ctx, key(service, entityKey), raw, s.ttl).Err()
}
