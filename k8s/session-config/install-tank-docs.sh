install_tank_docs() {
  local config_dir="${INSTALL_TANK_DOCS_CONFIG_DIR:-/opt/tank/session-config}"
  local dest_root="${INSTALL_TANK_DOCS_DEST_ROOT:-/workspace/.tank/docs}"
  local bundled_file base rel dest_path

  [ -d "$config_dir" ] || return 0
  mkdir -p "$dest_root"

  for bundled_file in "$config_dir"/docs__*; do
    [ -e "$bundled_file" ] || continue
    base="$(basename "$bundled_file")"
    rel="${base#docs__}"
    rel="${rel//__/\/}"
    dest_path="$dest_root/$rel"
    mkdir -p "$(dirname "$dest_path")"
    cp "$bundled_file" "$dest_path"
  done
}

# Legacy scripts may source this file and call install_tank_docs themselves.
# Session launch scripts execute it directly during pod boot.
if [ "$(basename "$0")" = "install-tank-docs.sh" ]; then
  install_tank_docs "$@"
fi
