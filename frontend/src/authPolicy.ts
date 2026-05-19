export type SessionRole = "admin" | "user" | "service";

export type GitHubRepoSource = "user_installation" | "host_installation" | "none";

export interface GitHubAccess {
  repo_source: GitHubRepoSource;
  can_list_repos: boolean;
  requires_onboarding: boolean;
}

export interface GitHubOnboardingUser {
  role: SessionRole;
  installation_id: number | null;
  github_access: GitHubAccess;
}

export function requiresGitHubOnboarding(user: GitHubOnboardingUser): boolean {
  return user.github_access.requires_onboarding;
}
