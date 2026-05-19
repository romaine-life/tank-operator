package main

import (
	"os"
	"strings"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/profiles"
)

const (
	githubRepoSourceUser = "user_installation"
	githubRepoSourceHost = "host_installation"
	githubRepoSourceNone = "none"
)

type githubAccessResponse struct {
	RepoSource         string `json:"repo_source"`
	CanListRepos       bool   `json:"can_list_repos"`
	RequiresOnboarding bool   `json:"requires_onboarding"`
}

func githubAccessForUser(email, role string, profile profiles.Profile) githubAccessResponse {
	source := githubRepoSourceNone
	if profile.InstallationID != nil {
		source = githubRepoSourceUser
	} else if githubUsesHostInstallation(email) {
		source = githubRepoSourceHost
	}
	canList := source != githubRepoSourceNone
	return githubAccessResponse{
		RepoSource:         source,
		CanListRepos:       canList,
		RequiresOnboarding: role == auth.RoleUser && !canList,
	}
}

func githubUsesHostInstallation(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	hostEmail := strings.ToLower(strings.TrimSpace(os.Getenv("HOST_EMAIL")))
	return hostEmail != "" && email == hostEmail
}
