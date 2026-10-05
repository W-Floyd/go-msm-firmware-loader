#!/bin/sh
# Compare two msm-firmware-loader trees, symlink targets included.
#
#   compare-targets.sh BASE_A BASE_B
#
# BASE_A and BASE_B are the loaders' base directories (for example
# /run/msm-firmware-loader and /run/mfl-go). Each tree is listed without
# following symlinks, with the base path replaced by @BASE@ in the links'
# targets, so that trees mounted at different bases compare equal. The lists
# are then compared with comm(1): busybox diff is unified and awkward.
#
#   compare-targets.sh dump BASE     print one tree's list (what is compared)
#
# Plain POSIX sh plus find, readlink, sort and comm, so it runs on busybox.

dump() {
	base=$1
	# mnt/ names show which partitions were mounted (a failed mount leaves
	# an empty directory); what is in them is reached through target/.
	(cd "$base" && ls mnt | sed 's,^,M mnt/,')
	(cd "$base/target" && find . | sort | while IFS= read -r p; do
		if [ -h "$p" ]; then
			printf 'L %s -> %s\n' "$p" "$(readlink "$p" | sed "s,$base,@BASE@,g")"
		elif [ -d "$p" ]; then
			printf 'D %s\n' "$p"
		else
			printf 'F %s\n' "$p"
		fi
	done)
}

if [ "$1" = dump ] && [ $# -eq 2 ]; then
	dump "$2"
	exit
fi
if [ $# -ne 2 ]; then
	echo "usage: $0 BASE_A BASE_B | $0 dump BASE" >&2
	exit 2
fi

tmp=${TMPDIR:-/tmp}/compare-targets.$$
mkdir -p "$tmp" || exit 2
trap 'rm -rf "$tmp"' EXIT
dump "$1" | sort >"$tmp/a"
dump "$2" | sort >"$tmp/b"

echo "A $1: $(wc -l <"$tmp/a") entries; B $2: $(wc -l <"$tmp/b") entries"
comm -23 "$tmp/a" "$tmp/b" >"$tmp/only-a"
comm -13 "$tmp/a" "$tmp/b" >"$tmp/only-b"
# A path in both lists with different content is a "differing" entry, not
# one missing and one extra.
cut -c3- "$tmp/only-a" | sed 's/ -> .*//' | sort >"$tmp/pa"
cut -c3- "$tmp/only-b" | sed 's/ -> .*//' | sort >"$tmp/pb"
comm -12 "$tmp/pa" "$tmp/pb" >"$tmp/both"

status=0
echo "== only in A (missing from B)"
while IFS= read -r l; do
	p=$(echo "$l" | cut -c3- | sed 's/ -> .*//')
	grep -qxF "$p" "$tmp/both" || { echo "  $l"; status=1; }
done <"$tmp/only-a"
echo "== only in B (extra)"
while IFS= read -r l; do
	p=$(echo "$l" | cut -c3- | sed 's/ -> .*//')
	grep -qxF "$p" "$tmp/both" || { echo "  $l"; status=1; }
done <"$tmp/only-b"
echo "== differing"
while IFS= read -r p; do
	echo "  $p"
	grep -F -- "$p" "$tmp/only-a" | grep -E "^. $(printf '%s' "$p" | sed 's/[][\.*^$/]/\\&/g')( |$)" | sed 's/^/    A: /'
	grep -F -- "$p" "$tmp/only-b" | grep -E "^. $(printf '%s' "$p" | sed 's/[][\.*^$/]/\\&/g')( |$)" | sed 's/^/    B: /'
	status=1
done <"$tmp/both"
[ $status -eq 0 ] && echo "trees match"
exit $status
