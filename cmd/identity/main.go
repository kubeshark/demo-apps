package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/kubeshark/demo-apps/internal/flows/identity"
	"github.com/kubeshark/demo-apps/internal/httpx"
)

func main() {
	ctx := context.Background()
	svc, err := identity.NewService(ctx, identity.ServiceConfig{
		LDAPAddr:       httpx.Env("LDAP_ADDR", "openldap:389"),
		BaseDN:         httpx.Env("LDAP_BASE_DN", "dc=demo,dc=local"),
		AdminPassword:  httpx.MustEnv("LDAP_ADMIN_PASSWORD"),
		RadiusAuthAddr: httpx.Env("RADIUS_AUTH_ADDR", "freeradius:1812"),
		RadiusAcctAddr: httpx.Env("RADIUS_ACCT_ADDR", "freeradius:1813"),
		RadiusSecret:   httpx.MustEnv("RADIUS_SECRET"),
	})
	if err != nil {
		slog.Error("startup", "err", err)
		os.Exit(1)
	}
	if err := httpx.Serve(httpx.Env("LISTEN_ADDR", ":8080"), svc.Handler()); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
