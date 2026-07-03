#!/bin/sh
set -e

case "$1" in
  remove|deconfigure)
    if [ -d /run/systemd/system ]; then
      systemctl stop gitcote.service >/dev/null 2>&1 || true
      systemctl disable gitcote.service >/dev/null 2>&1 || true
    fi
    ;;
esac

exit 0
