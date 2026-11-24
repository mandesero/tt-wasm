#!/bin/sh
set -eu

snapshot=$1
shift
sources=$(mktemp --suffix=.sources)
trap 'rm -f "$sources"' EXIT
sed     -e "s|http://deb.debian.org/debian-security|http://snapshot.debian.org/archive/debian-security/$snapshot|"     -e "s|http://deb.debian.org/debian|http://snapshot.debian.org/archive/debian/$snapshot|"     /etc/apt/sources.list.d/debian.sources > "$sources"
chmod 644 "$sources"

apt_snapshot() {
    apt-get -o Acquire::Check-Valid-Until=false -o Acquire::Retries=3         -o Dir::Etc::sourcelist="$sources" -o Dir::Etc::sourceparts=- "$@"
}

apt_snapshot update
DEBIAN_FRONTEND=noninteractive apt_snapshot install -y --no-install-recommends "$@"
rm -rf /var/lib/apt/lists/*
