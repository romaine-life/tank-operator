package main

import (
	"os"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
)

func hostAdminEmail() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv("HOST_EMAIL")))
}

func configuredSuperAdmins() map[string]bool {
	return parseEmailSet(envDefault("SUPER_ADMIN_EMAILS", hostAdminEmail()))
}

func hasAdminPower(user auth.User) bool {
	if user.Role == auth.RoleAdmin {
		return true
	}
	if user.Role != auth.RoleService {
		return false
	}
	actorEmail := strings.ToLower(strings.TrimSpace(user.ActorEmail))
	return actorEmail != "" && configuredSuperAdmins()[actorEmail]
}
