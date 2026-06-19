skill_targets_for_scope() {
  case "$1" in
    common) printf '%s\n' "claude codex" ;;
    claude) printf '%s\n' "claude" ;;
    codex) printf '%s\n' "codex" ;;
    *) return 1 ;;
  esac
}

install_tank_skills() {
  config_dir="${INSTALL_TANK_SKILLS_CONFIG_DIR:-/opt/tank/session-config}"
  [ -d "$config_dir" ] || return 0
  mkdir -p "$HOME/.claude/skills" "$HOME/.codex/skills"

  for bundled_file in "$config_dir"/skills__*; do
    [ -e "$bundled_file" ] || continue
    base="$(basename "$bundled_file")"
    rest="${base#skills__}"
    scope="${rest%%__*}"
    encoded_path="${rest#"$scope"__}"
    skill="${encoded_path%%__*}"
    rel="${encoded_path#"$skill"}"
    rel="${rel#__}"
    while [ "$rel" != "${rel%__*}" ]; do
      rel="${rel%%__*}/${rel#*__}"
    done
    targets="$(skill_targets_for_scope "$scope")" || continue
    for target in $targets; do
      case "$target" in
        claude) root="$HOME/.claude/skills" ;;
        codex) root="$HOME/.codex/skills" ;;
        *) continue ;;
      esac
      dest_path="$root/$skill/$rel"
      mkdir -p "$(dirname "$dest_path")"
      cp "$bundled_file" "$dest_path"
    done
  done
}

# Legacy scripts source this file and call install_tank_skills themselves.
# SDK runner launch scripts execute it directly during pod boot.
if [ "$(basename "$0")" = "install-tank-skills.sh" ]; then
  install_tank_skills "$@"
fi
