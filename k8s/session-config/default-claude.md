You're inside a pod provisioned as part of the romaine-life/tank-operator repo. These are called "sessions" within the tank-operator ecosystem. You don't always need to consult that repo, but it explains the nature of this runtime environment.

Tank-provided policy docs are materialized at `/workspace/.tank/docs/`.
When a prompt asks about quality timeframes or migration policy, read those
workspace copies instead of assuming the cloned repo carries those files.

Tank-operator sessions are ephemeral and are curated for your needs. The user has the option to choose which repos will be pre-cloned in your workspace. If the user's statement seems vague or ambiguous and jarringly starts mentioning features you don't recognize, that's likely a sign that you are going to want to look at what git repos exist locally, and start inspecting those. You are also encouraged to clone any and all in-scope repos that you deem necessary if the local repos don't answer your question, and there are many cases where solving a problem touches multiple repos.

You should have freedom to read, and sometimes write, against almost all relevant infra. You're in a k8s cluster, and it leverages argocd. Your service account should have permissions needed to solve problems, and argocd and azure and all various tools should exist in the mcp servers. The mcp servers are hand-rolled by us, so expect them to afford us the ability to do what we need. If not, that's a gap worth raising, and don't be surprised if you get asked to fix it on the spot.

This cluster has a self-rolled identity provider called "auth.romaine.life". Pods get projected service account tokens with that audience. You are expected to use that token to authenticate to everything within this ecosystem. If that is not possible, raise the concern.

The k8s cluster and most core infra is provisioned from romaine-life/infra-bootstrap.

Session pods intentionally do not ship Docker or a container runtime. Do not
report "I couldn't run docker build because Docker is not installed" as a
blocker unless the user specifically asked for a local image build. For normal
changes, run the relevant language/tooling checks available in the pod
(`pytest`, `npm`, `go test`, `helm template`, etc.). The normal container
build gate is the repo's PR CI: `.github/workflows/docker-build-check.yml`
runs a throwaway Docker build with `push: false`. If a change touches
Dockerfiles, image build inputs, lockfiles, entrypoints, launcher scripts,
baked assets, or Helm image wiring and you need image-build feedback before a
PR is ready, trigger that workflow manually with `git_ref`. Release/deploy
workflows are the only image-publishing path. If you truly need an image build
from inside Kubernetes, use or propose a dedicated builder path (GitHub
Actions, ACR Tasks, BuildKit/Kaniko/buildah in an appropriately privileged
builder pod), not ad hoc Docker-in-this-session-pod.

For shell `git` access, call the GitHub MCP `mint_clone_token` tool for the needed repo(s), then use the returned token in an `https://x-access-token:<token>@github.com/owner/
repo.git` remote URL. The github MCP server has additional tools if the minted token lacks permissions. If the combination of both lacks permissions, the solve is likely to increase the scope of the mcp server or the token, so feel free to raise the issue (and maybe even draft a code change on a branch) that expands the permissions on the mcp server or the minted token.

You need to install from the lockfile before doing frontend builds if you just cloned the repo.

If you need to build a container with `docker`, that is delegated out to github actions. This gets around running docker in docker, since you're in a container currently. There should be an obvious github action you can use to test builds. When in the normal process of testing code against a feature branch, you're encouraged to freely push your code so github actions can run against it and test builds when you feel compelled to run docker.
