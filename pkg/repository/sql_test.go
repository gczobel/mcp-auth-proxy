package repository

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/ory/fosite"
)

func TestSQLRepositoryAccessTokenSession(t *testing.T) {
	repo, err := NewSQLRepository("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("failed to create sql repository: %v", err)
	}
	defer repo.Close()

	ctx := context.Background()
	client := &fosite.DefaultClient{
		ID:           "client-1",
		Secret:       []byte("secret"),
		RedirectURIs: []string{"https://example.com/callback"},
	}

	req := &fosite.Request{
		ID:             "req-1",
		RequestedAt:    time.Now().UTC().Round(time.Second),
		Client:         client,
		RequestedScope: []string{"scope.read"},
		Form:           url.Values{"code": {"value"}},
	}

	if err := repo.CreateAccessTokenSession(ctx, "sig-1", req); err != nil {
		t.Fatalf("CreateAccessTokenSession failed: %v", err)
	}

	result, err := repo.GetAccessTokenSession(ctx, "sig-1", &fosite.DefaultSession{})
	if err != nil {
		t.Fatalf("GetAccessTokenSession failed: %v", err)
	}

	retrievedReq := result.(*fosite.Request)
	if retrievedReq.ID != req.ID {
		t.Fatalf("expected request ID %s, got %s", req.ID, retrievedReq.ID)
	}
	if retrievedReq.Client.GetID() != client.GetID() {
		t.Fatalf("expected client ID %s, got %s", client.GetID(), retrievedReq.Client.GetID())
	}
	if len(retrievedReq.RequestedScope) != 1 || retrievedReq.RequestedScope[0] != "scope.read" {
		t.Fatalf("unexpected requested scope: %#v", retrievedReq.RequestedScope)
	}
}

func TestSQLRepositorySessionPersistence(t *testing.T) {
	repo, err := NewSQLRepository("sqlite", "file::memory:?cache=shared&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("failed to create sql repository: %v", err)
	}
	defer repo.Close()

	sess := &fosite.DefaultSession{
		Username: "test-user",
		Subject:  "test-user",
	}

	ctx := context.Background()
	client := &fosite.DefaultClient{
		ID:           "client-sess",
		Secret:       []byte("secret"),
		RedirectURIs: []string{"https://example.com/callback"},
	}

	req := &fosite.Request{
		ID:             "req-sess",
		RequestedAt:    time.Now().UTC().Round(time.Second),
		Client:         client,
		RequestedScope: []string{"openid"},
		Form:           url.Values{"code": {"value"}},
		Session:        sess,
	}

	if err := repo.CreateAuthorizeCodeSession(ctx, "code-sess", req); err != nil {
		t.Fatalf("CreateAuthorizeCodeSession failed: %v", err)
	}

	result, err := repo.GetAuthorizeCodeSession(ctx, "code-sess", &fosite.DefaultSession{})
	if err != nil {
		t.Fatalf("GetAuthorizeCodeSession failed: %v", err)
	}

	restored := result.GetSession().(*fosite.DefaultSession)
	if restored.GetSubject() != "test-user" {
		t.Fatalf("expected subject 'test-user', got '%s'", restored.GetSubject())
	}
	if restored.GetUsername() != "test-user" {
		t.Fatalf("expected username 'test-user', got '%s'", restored.GetUsername())
	}
}

func TestSQLRepositorySessionPersistence_NilSessionStored(t *testing.T) {
	repo, err := NewSQLRepository("sqlite", "file::memory:?cache=shared&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("failed to create sql repository: %v", err)
	}
	defer repo.Close()

	ctx := context.Background()
	client := &fosite.DefaultClient{
		ID:           "client-old",
		Secret:       []byte("secret"),
		RedirectURIs: []string{"https://example.com/callback"},
	}

	req := &fosite.Request{
		ID:             "req-old",
		RequestedAt:    time.Now().UTC().Round(time.Second),
		Client:         client,
		RequestedScope: []string{"openid"},
		Form:           url.Values{"code": {"value"}},
	}

	if err := repo.CreateAuthorizeCodeSession(ctx, "code-old", req); err != nil {
		t.Fatalf("CreateAuthorizeCodeSession failed: %v", err)
	}

	result, err := repo.GetAuthorizeCodeSession(ctx, "code-old", &fosite.DefaultSession{})
	if err != nil {
		t.Fatalf("GetAuthorizeCodeSession failed: %v", err)
	}

	if result.GetSession() != nil {
		t.Fatalf("expected nil session when no session was stored, got %v", result.GetSession())
	}
}

func TestSQLRepositoryUnsupportedDriver(t *testing.T) {
	if _, err := NewSQLRepository("unsupported", "dsn"); err == nil {
		t.Fatalf("expected error for unsupported driver but got nil")
	}
}

func TestGetClientName(t *testing.T) {
	for _, tc := range []struct {
		name    string
		newRepo func() (Repository, error)
	}{
		{"kvs", func() (Repository, error) { return NewKVSRepository(t.TempDir()+"/clients.db", "test") }},
		{"sql", func() (Repository, error) { return NewSQLRepository("sqlite", "file::memory:?cache=shared") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, err := tc.newRepo()
			if err != nil {
				t.Fatalf("failed to create repository: %v", err)
			}
			defer repo.Close()

			ctx := context.Background()
			client := &fosite.DefaultClient{
				ID:           "client-name-test",
				Secret:       []byte("secret"),
				RedirectURIs: []string{"https://example.com/callback"},
			}

			if err := repo.RegisterClient(ctx, client, "Display Name"); err != nil {
				t.Fatalf("RegisterClient failed: %v", err)
			}
			got, err := repo.GetClientName(ctx, "client-name-test")
			if err != nil {
				t.Fatalf("GetClientName failed: %v", err)
			}
			if got != "Display Name" {
				t.Fatalf("expected name %q, got %q", "Display Name", got)
			}

			// Missing client surfaces fosite.ErrNotFound, matching the
			// existing GetClient contract on both backends.
			if _, err := repo.GetClientName(ctx, "missing"); err != fosite.ErrNotFound {
				t.Fatalf("expected fosite.ErrNotFound for missing client, got %v", err)
			}
		})
	}
}

func TestGetClientNameCorruptRecord(t *testing.T) {
	repo, err := NewSQLRepository("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("failed to create repository: %v", err)
	}
	defer repo.Close()

	ctx := context.Background()
	client := &fosite.DefaultClient{
		ID:           "client-corrupt",
		Secret:       []byte("secret"),
		RedirectURIs: []string{"https://example.com/callback"},
	}
	if err := repo.RegisterClient(ctx, client, "Name"); err != nil {
		t.Fatalf("RegisterClient failed: %v", err)
	}

	// Corrupt the stored client blob so unmarshal must fail.
	raw, err := repo.(*sqlRepository).db.DB()
	if err != nil {
		t.Fatalf("failed to get underlying DB: %v", err)
	}
	if _, err := raw.Exec("UPDATE client_records SET client = 'not-json' WHERE id = ?", "client-corrupt"); err != nil {
		t.Fatalf("failed to corrupt record: %v", err)
	}

	if _, err := repo.GetClientName(ctx, "client-corrupt"); err == nil {
		t.Fatalf("expected error for corrupt client record, got nil")
	}
}
