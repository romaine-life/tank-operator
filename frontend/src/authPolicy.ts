export type SessionRole = "admin" | "user" | "service";

export interface GitHubOnboardingUser {
  role: SessionRole;
  installation_id: number | null;
}

export function requiresGitHubOnboarding(user: GitHubOnboardingUser): boolean {
  return user.installation_id == null && user.role === "user";
}
