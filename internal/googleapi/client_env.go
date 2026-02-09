package googleapi

import (
	"context"
	"net/http"
	"os"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	envClientID     = "GOG_CLIENT_ID"
	envClientSecret = "GOG_CLIENT_SECRET"
	envRefreshToken = "GOG_REFRESH_TOKEN"
)

var envTokenSource = tokenSourceFromEnv

func tokenSourceFromEnv(ctx context.Context, scopes []string) (oauth2.TokenSource, bool, error) {
	clientID := strings.TrimSpace(os.Getenv(envClientID))
	clientSecret := strings.TrimSpace(os.Getenv(envClientSecret))
	refreshToken := strings.TrimSpace(os.Getenv(envRefreshToken))

	if clientID == "" || clientSecret == "" || refreshToken == "" {
		return nil, false, nil
	}

	cfg := oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       scopes,
	}

	ctx = context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: defaultHTTPTimeout})

	ts := cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	return ts, true, nil
}
