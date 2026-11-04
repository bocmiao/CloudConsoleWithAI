#!/bin/sh
# Makes the Linux install package from a built program:
#   deploy/linux/package.sh dist/MiaoPanel-linux-amd64 dist/MiaoPanel-linux-amd64.tar.gz
# The package unpacks to miaopanel/: the program, install.sh, uninstall.sh
# and README.txt.
set -eu
[ $# -eq 2 ] || { echo "用法：$0 程序 安装包.tar.gz" >&2; exit 2; }
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir "$work/miaopanel"
cp "$1" "$work/miaopanel/miaopanel"
cp "$here/install.sh" "$here/uninstall.sh" "$here/README.txt" "$work/miaopanel/"
chmod 755 "$work/miaopanel/miaopanel" "$work/miaopanel/install.sh" "$work/miaopanel/uninstall.sh"
chmod 644 "$work/miaopanel/README.txt"
tar -C "$work" --owner=0 --group=0 --numeric-owner -czf "$2" miaopanel
