#!/bin/sh
# Puts the plugin this image ships among Jellyfin's plugins, as the only
# version of it there (Jellyfin would load the newest of several), then
# runs Jellyfin in this script's place.
set -eu

data=${JELLYFIN_DATA_DIR:-/config}
shipped=/usr/lib/overfin/jellyfin-plugin
version=$(ls "$shipped")
case "$version" in
*[!0-9.]* | "") echo "overfin-jellyfin: the image ships no plugin, or more than one" >&2; exit 1 ;;
esac

target="$data/plugins/Overfin_$version.0"
mkdir -p "$data/plugins"
for old in "$data"/plugins/Overfin_*; do
	if [ -e "$old" ] && [ "$old" != "$target" ]; then
		rm -rf "$old"
	fi
done
mkdir -p "$target"
cp "$shipped/$version"/* "$target/"
echo "overfin-jellyfin: Overfin's plugin $version is in place"

exec /jellyfin/jellyfin "$@"
