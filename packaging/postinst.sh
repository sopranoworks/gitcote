#!/bin/sh
set -e

case "$1" in
  configure)
    if ! getent group gitcote >/dev/null 2>&1; then
      addgroup --system gitcote
    fi
    if ! getent passwd gitcote >/dev/null 2>&1; then
      adduser --system --ingroup gitcote --home /var/lib/gitcote \
        --no-create-home --shell /usr/sbin/nologin gitcote
    fi

    if [ -d /var/lib/gitcote ]; then
      chown -R gitcote:gitcote /var/lib/gitcote || true
      chmod 0750 /var/lib/gitcote || true
    fi

    if [ -f /etc/gitcote/gitcote.yaml ]; then
      chown root:gitcote /etc/gitcote/gitcote.yaml || true
      chmod 0640 /etc/gitcote/gitcote.yaml || true
    fi

    if [ -d /run/systemd/system ]; then
      systemctl daemon-reload || true
    fi

    echo "gitcote: installed. Edit /etc/gitcote/gitcote.yaml, then enable + start with:"
    echo "           sudo systemctl enable --now gitcote"
    ;;
esac

exit 0
