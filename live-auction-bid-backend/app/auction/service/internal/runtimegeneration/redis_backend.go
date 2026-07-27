package runtimegeneration

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

type RedisBackend struct {
	client redis.UniversalClient
}

func NewRedisBackend(client redis.UniversalClient) (*RedisBackend, error) {
	if client == nil {
		return nil, errors.New("runtime generation Redis client is required")
	}
	return &RedisBackend{client: client}, nil
}

func (backend *RedisBackend) PrimaryIdentity(ctx context.Context) (string, error) {
	if backend == nil || backend.client == nil {
		return "", errors.New("runtime generation Redis backend is not initialized")
	}
	info, err := backend.client.Info(ctx, "server", "replication").Result()
	if err != nil {
		return "", fmt.Errorf("INFO server replication: %w", err)
	}
	return ParsePrimaryIdentity(info)
}

func (backend *RedisBackend) VerifiedGeneration(ctx context.Context) (string, error) {
	if backend == nil || backend.client == nil {
		return "", errors.New("runtime generation Redis backend is not initialized")
	}
	value, err := backend.client.Get(ctx, VerifiedGenerationKey).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func (backend *RedisBackend) SetVerifiedGeneration(ctx context.Context, generation string) error {
	if backend == nil || backend.client == nil {
		return errors.New("runtime generation Redis backend is not initialized")
	}
	generation = strings.TrimSpace(generation)
	if !validRunID(generation) {
		return errors.New("verified Redis generation is invalid")
	}
	return backend.client.Set(ctx, VerifiedGenerationKey, generation, 0).Err()
}

func ParsePrimaryIdentity(info string) (string, error) {
	fields := make(map[string]string)
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if ok {
			fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if fields["role"] != "master" {
		return "", fmt.Errorf("redis endpoint role=%q, expected master", fields["role"])
	}
	runID := fields["run_id"]
	if !validRunID(runID) {
		return "", errors.New("redis INFO omitted a valid run_id")
	}
	return runID, nil
}

func validRunID(value string) bool {
	if len(value) != 40 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
