#!/bin/sh
set -eu

TARGETARCH="${TARGETARCH:-amd64}"
. /opt/tank/session-images/versions.env

case "${TARGETARCH}" in
  amd64)
    uv_arch="x86_64"
    ;;
  arm64)
    uv_arch="aarch64"
    ;;
  *)
    echo "unsupported TARGETARCH=${TARGETARCH}" >&2
    exit 1
    ;;
esac

apk add --no-cache \
  bash \
  ca-certificates \
  curl \
  git \
  github-cli \
  gnupg \
  jq \
  less \
  make \
  openssh-client \
  py3-pip \
  python3 \
  ripgrep \
  unzip \
  vim

curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
  | tar -C /usr/local -xz
printf '%s\n' 'export PATH="/usr/local/go/bin:${PATH}"' > /etc/profile.d/go-path.sh

curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
  -o /usr/local/bin/kubectl
chmod +x /usr/local/bin/kubectl

curl -fsSL "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}/tofu_${TOFU_VERSION}_linux_${TARGETARCH}.zip" \
  -o /tmp/tofu.zip
unzip -q /tmp/tofu.zip tofu -d /usr/local/bin
chmod +x /usr/local/bin/tofu
rm /tmp/tofu.zip

curl -fsSL "https://get.helm.sh/helm-${HELM_VERSION}-linux-${TARGETARCH}.tar.gz" \
  | tar -xz -C /tmp
mv "/tmp/linux-${TARGETARCH}/helm" /usr/local/bin/helm
rm -rf "/tmp/linux-${TARGETARCH}"
chmod +x /usr/local/bin/helm

curl -fsSL "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_${TARGETARCH}" \
  -o /usr/local/bin/yq
chmod +x /usr/local/bin/yq

curl -fsSL "https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/uv-${uv_arch}-unknown-linux-musl.tar.gz" \
  | tar -xz -C /tmp
mv "/tmp/uv-${uv_arch}-unknown-linux-musl/uv" /usr/local/bin/uv
mv "/tmp/uv-${uv_arch}-unknown-linux-musl/uvx" /usr/local/bin/uvx
rm -rf "/tmp/uv-${uv_arch}-unknown-linux-musl"
chmod +x /usr/local/bin/uv /usr/local/bin/uvx

curl -fsSL "https://pkgs.tailscale.com/stable/tailscale_${TAILSCALE_VERSION}_${TARGETARCH}.tgz" \
  | tar -xz -C /tmp
mv "/tmp/tailscale_${TAILSCALE_VERSION}_${TARGETARCH}/tailscale" /usr/local/bin/tailscale
mv "/tmp/tailscale_${TAILSCALE_VERSION}_${TARGETARCH}/tailscaled" /usr/local/bin/tailscaled
rm -rf "/tmp/tailscale_${TAILSCALE_VERSION}_${TARGETARCH}"
chmod +x /usr/local/bin/tailscale /usr/local/bin/tailscaled

npm install -g \
  "@sandbox-agent/cli@${SANDBOX_AGENT_VERSION}" \
  "vite@${VITE_VERSION}" \
  "typescript@${TYPESCRIPT_VERSION}"

pip install --no-cache-dir --break-system-packages \
  "pytest==${PYTEST_VERSION}" \
  "ruff==${RUFF_VERSION}"
