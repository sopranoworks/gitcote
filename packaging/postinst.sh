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

    # Print credential setup notice on fresh install only ($2 is empty)
    if [ -z "$2" ]; then
      echo "" >&2
      echo "=== GitCote post-install notice ===" >&2
      echo "" >&2
      echo "To enable AI agent spawning, authenticate CLI tools" >&2
      echo "under the gitcote service user:" >&2
      echo "" >&2
      echo "  sudo -u gitcote claude login        # Claude Code" >&2
      echo "  sudo -u gitcote gemini auth login   # Gemini CLI" >&2
      echo "" >&2
      echo "For OpenAI Codex, set OPENAI_API_KEY via:" >&2
      echo "  sudo systemctl edit gitcote" >&2
      echo "" >&2
      echo "This is required once per tool." >&2
      echo "===================================" >&2
      echo "" >&2
    fi
    ;;
esac

exit 0
