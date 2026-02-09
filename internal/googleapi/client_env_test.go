package googleapi

import (
	"context"
	"errors"
	"testing"

	"golang.org/x/oauth2"

	"github.com/steipete/gogcli/internal/config"
	"github.com/steipete/gogcli/internal/secrets"
)

func TestTokenSourceFromEnv_AllSet(t *testing.T) {
	t.Setenv(envClientID, "cid")
	t.Setenv(envClientSecret, "csecret")
	t.Setenv(envRefreshToken, "rtoken")

	ts, ok, err := tokenSourceFromEnv(context.Background(), []string{"scope1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if ts == nil {
		t.Fatal("expected non-nil token source")
	}
}

func TestTokenSourceFromEnv_MissingRefreshToken(t *testing.T) {
	t.Setenv(envClientID, "cid")
	t.Setenv(envClientSecret, "csecret")
	// GOG_REFRESH_TOKEN not set

	ts, ok, err := tokenSourceFromEnv(context.Background(), []string{"scope1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false")
	}
	if ts != nil {
		t.Fatal("expected nil token source")
	}
}

func TestTokenSourceFromEnv_MissingClientID(t *testing.T) {
	// GOG_CLIENT_ID not set
	t.Setenv(envClientSecret, "csecret")
	t.Setenv(envRefreshToken, "rtoken")

	ts, ok, err := tokenSourceFromEnv(context.Background(), []string{"scope1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false")
	}
	if ts != nil {
		t.Fatal("expected nil token source")
	}
}

func TestTokenSourceFromEnv_WhitespaceOnly(t *testing.T) {
	t.Setenv(envClientID, "  ")
	t.Setenv(envClientSecret, "csecret")
	t.Setenv(envRefreshToken, "rtoken")

	ts, ok, err := tokenSourceFromEnv(context.Background(), []string{"scope1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false when client ID is whitespace-only")
	}
	if ts != nil {
		t.Fatal("expected nil token source")
	}
}

func TestOptionsForAccountScopes_EnvVarPreferred(t *testing.T) {
	origEnvTS := envTokenSource
	origRead := readClientCredentials
	origOpen := openSecretsStore

	t.Cleanup(func() {
		envTokenSource = origEnvTS
		readClientCredentials = origRead
		openSecretsStore = origOpen
	})

	called := false
	envTokenSource = func(_ context.Context, scopes []string) (oauth2.TokenSource, bool, error) {
		called = true
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "env-token"}), true, nil
	}

	readClientCredentials = func(string) (config.ClientCredentials, error) {
		t.Fatal("readClientCredentials should not be called when env vars are set")
		return config.ClientCredentials{}, nil
	}
	openSecretsStore = func() (secrets.Store, error) {
		t.Fatal("openSecretsStore should not be called when env vars are set")
		return nil, errors.New("should not be called")
	}

	opts, err := optionsForAccountScopes(context.Background(), "svc", "a@b.com", []string{"s1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected envTokenSource to be called")
	}
	if len(opts) == 0 {
		t.Fatal("expected client options")
	}
}
