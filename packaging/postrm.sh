#!/bin/sh
set -e

case "$1" in
  purge)
    rm -rf /var/lib/gitcote
    if getent passwd gitcote >/dev/null 2>&1; then
      deluser --system gitcote >/dev/null 2>&1 || true
    fi
    if getent group gitcote >/dev/null 2>&1; then
      delgroup --system gitcote >/dev/null 2>&1 || true
    fi
    if [ -d /run/systemd/system ]; then
      systemctl daemon-reload || true
    fi
    ;;
  remove|upgrade|failed-upgrade|abort-install|abort-upgrade|disappear)
    ;;
esac

exit 0
