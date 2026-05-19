# Repo Selection

The splash-page repo picker has two server-owned data sources:

- `GET /api/github/recent-repos` reads durable `sessions.repos` rows for the
  signed-in owner and returns recently selected `owner/name` slugs.
- `GET /api/github/repos` asks mcp-github to enumerate the caller's resolved
  GitHub repo source and returns `{repos, repo_source}` to the SPA.

Repo discovery capability is explicit in `/api/auth/me` and
`/api/auth/exchange` under `github_access`:

```json
{
  "repo_source": "user_installation",
  "can_list_repos": true,
  "requires_onboarding": false
}
```

`repo_source` is one of:

- `user_installation`: the profile has a user-facing GitHub App
  `installation_id`.
- `host_installation`: the signed-in email matches `HOST_EMAIL`, so mcp-github
  can route through the host GitHub App installation without a profile
  installation id.
- `none`: repo discovery cannot enumerate GitHub repositories for this caller.

The frontend uses `github_access.requires_onboarding` for the onboarding wall
and `github_access.can_list_repos` for the picker. It must not infer repo
access from `role` plus `installation_id`; `role=admin` is not itself a GitHub
token source.

`/api/github/repos` returns structured errors with a stable `code` and
human-readable `detail`, including:

- `github_installation_required`
- `mcp_not_configured`
- `github_auth_exchange_failed`
- `github_repo_discovery_failed`

Manual `owner/name` entry remains available in the picker when discovery fails.
