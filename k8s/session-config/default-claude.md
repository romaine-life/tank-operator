You're inside a pod provisioned as part of the nelsong6/tank-operator repo. You don't always need to consult that repo, but it explains the nature of this runtime environment.

At the start of most conversations, you'll want to list repos using the github mcp server, and see if you can determine which repo(s) are in scope for the user's question. Your workspace is intentionally empty at the start of interactions. This is part of a process that gives you a clean 'worktree' each new spawn, by design. You are encouraged to clone any and all in-scope repos that you deem necessary.

You should have freedom to read, and sometimes write, against almost all relevant infra. You're in a k8s cluster, and it leverages argocd. Your service account should have permissions needed to solve problems, and argocd and azure and all various tools should exist in the mcp servers. The mcp servers are hand-rolled by us, so expect them to afford us the ability to do what we need. If not, that's a gap worth raising, and don't be surprised if you get asked to fix it on the spot.

The k8s cluster and most core infra is provisioned from nelsong6/infra-bootstrap.

For shell `git` access, call the GitHub MCP `mint_clone_token` tool for the needed repo(s), then use the returned token in an `https://x-access-token:<token>@github.com/owner/
repo.git` remote URL. The github MCP server has additional tools if the minted token lacks permissions. If the combination of both lacks permissions, the solve is likely to increase the scope of the mcp server or the token, so feel free to raise the issue (and maybe even draft a code change on a branch) that expands the permissions on the mcp server or the minted token.

If you need to build a container with `docker`, that is delegated out to github actions. This gets around running docker in docker, since you're in a container currently. There should be an obvious github action you can use to test builds. When in the normal process of testing code against a feature branch, you're encouraged to freely push your code so github actions can run against it and test builds when you feel compelled to run docker.
