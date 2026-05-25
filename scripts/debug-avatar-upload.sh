#!/usr/bin/env sh
set -eu

usage() {
  printf '%s\n' "usage: $0 <base-url> <jwt> <kind> <name> <avatar-file> <backing-file>" >&2
  printf '%s\n' "example: $0 https://tank.romaine.life \"\$JWT\" agent Ada ./avatar.png ./source.png" >&2
}

if [ "$#" -ne 6 ]; then
  usage
  exit 2
fi

base_url=${1%/}
jwt=$2
kind=$3
name=$4
avatar_file=$5
backing_file=$6

if [ ! -f "$avatar_file" ]; then
  printf 'avatar file not found: %s\n' "$avatar_file" >&2
  exit 2
fi

if [ ! -f "$backing_file" ]; then
  printf 'backing file not found: %s\n' "$backing_file" >&2
  exit 2
fi

curl -sS \
  -H "Authorization: Bearer $jwt" \
  -F "kind=$kind" \
  -F "name=$name" \
  -F 'crop={"center_x":0.5,"center_y":0.5,"size":1}' \
  -F "avatar=@$avatar_file" \
  -F "backing=@$backing_file" \
  "$base_url/api/admin/avatars"
