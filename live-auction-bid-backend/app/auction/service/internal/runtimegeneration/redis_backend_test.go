package runtimegeneration

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRedisBackendValidatesInitializationAndCommands(t *testing.T) {
	if _, err := NewRedisBackend(nil); err == nil {
		t.Fatal("NewRedisBackend accepted nil client")
	}
	var nilBackend *RedisBackend
	if _, err := nilBackend.PrimaryIdentity(context.Background()); err == nil {
		t.Fatal("nil PrimaryIdentity returned no error")
	}
	if _, err := nilBackend.VerifiedGeneration(context.Background()); err == nil {
		t.Fatal("nil VerifiedGeneration returned no error")
	}
	if err := nilBackend.SetVerifiedGeneration(context.Background(), generationA); err == nil {
		t.Fatal("nil SetVerifiedGeneration returned no error")
	}

	info := "# Server\r\nrun_id:" + generationA + "\r\n# Replication\r\nrole:master\r\n"
	client := &redisClientStub{infoValue: info, getValue: "  " + generationA + "  "}
	backend, err := NewRedisBackend(client)
	if err != nil {
		t.Fatal(err)
	}
	if identity, err := backend.PrimaryIdentity(context.Background()); err != nil || identity != generationA {
		t.Fatalf("PrimaryIdentity=%q error=%v", identity, err)
	}
	if verified, err := backend.VerifiedGeneration(context.Background()); err != nil || verified != generationA {
		t.Fatalf("VerifiedGeneration=%q error=%v", verified, err)
	}
	if err := backend.SetVerifiedGeneration(context.Background(), "  "+generationB+" "); err != nil {
		t.Fatalf("SetVerifiedGeneration: %v", err)
	}
	if client.setKey != VerifiedGenerationKey || client.setValue != generationB || client.setExpiration != 0 {
		t.Fatalf("SET key=%q value=%q expiration=%s", client.setKey, client.setValue, client.setExpiration)
	}
	if err := backend.SetVerifiedGeneration(context.Background(), "invalid"); err == nil {
		t.Fatal("SetVerifiedGeneration accepted invalid generation")
	}
}

func TestRedisBackendPropagatesRedisAndIdentityErrors(t *testing.T) {
	sentinel := errors.New("Redis unavailable")
	client := &redisClientStub{infoErr: sentinel, getErr: redis.Nil, setErr: sentinel}
	backend, err := NewRedisBackend(client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.PrimaryIdentity(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("PrimaryIdentity error=%v want sentinel", err)
	}
	if verified, err := backend.VerifiedGeneration(context.Background()); err != nil || verified != "" {
		t.Fatalf("missing VerifiedGeneration=%q error=%v", verified, err)
	}
	if err := backend.SetVerifiedGeneration(context.Background(), generationA); !errors.Is(err, sentinel) {
		t.Fatalf("SetVerifiedGeneration error=%v want sentinel", err)
	}

	client.infoErr = nil
	client.infoValue = "role:replica\nrun_id:" + generationA
	if _, err := backend.PrimaryIdentity(context.Background()); err == nil {
		t.Fatal("PrimaryIdentity accepted replica INFO")
	}
	client.getErr = sentinel
	if _, err := backend.VerifiedGeneration(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("VerifiedGeneration error=%v want sentinel", err)
	}
}

type redisClientStub struct {
	redis.UniversalClient
	infoValue     string
	infoErr       error
	getValue      string
	getErr        error
	setErr        error
	setKey        string
	setValue      string
	setExpiration time.Duration
}

func (client *redisClientStub) Info(context.Context, ...string) *redis.StringCmd {
	return redis.NewStringResult(client.infoValue, client.infoErr)
}

func (client *redisClientStub) Get(context.Context, string) *redis.StringCmd {
	return redis.NewStringResult(client.getValue, client.getErr)
}

func (client *redisClientStub) Set(_ context.Context, key string, value interface{}, expiration time.Duration) *redis.StatusCmd {
	client.setKey = key
	client.setValue = value.(string)
	client.setExpiration = expiration
	return redis.NewStatusResult("OK", client.setErr)
}

func TestRedisBackendCoordinatesWriterAndFollowerGuards(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("AUCTION_TEST_REDIS_ADDR"))
	if address == "" {
		t.Skip("AUCTION_TEST_REDIS_ADDR is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := redis.NewClient(&redis.Options{Addr: address})
	defer func() { _ = client.Close() }()
	if err := client.Del(ctx, VerifiedGenerationKey).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cleanupCancel()
		_ = client.Del(cleanupCtx, VerifiedGenerationKey).Err()
	})
	backend, err := NewRedisBackend(client)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := backend.PrimaryIdentity(ctx)
	if err != nil || !validRunID(identity) {
		t.Fatalf("identity=%q error=%v", identity, err)
	}
	coordinator, err := NewGuard(backend, Config{Verify: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Initialize(ctx); err != nil {
		t.Fatalf("initialize coordinator: %v", err)
	}
	follower, err := NewGuard(backend, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := follower.Initialize(ctx); err != nil {
		t.Fatalf("initialize follower: %v", err)
	}
	if generation, err := follower.AllowWrite(); err != nil || generation != identity {
		t.Fatalf("follower generation=%q error=%v", generation, err)
	}
}
