package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/gorilla/websocket"
	"github.com/sopranoworks/shoka/pkg/uiws"
)

// wsManager is GitYard's thin /ws/ui WebSocket manager. It owns the connection
// upgrade and the request/response read loop, and delegates every message to the
// embedded *uiws.CoreHandlers — the reusable auth/user/OAuth slice extracted from
// Shoka for exactly this reuse. GitYard supplies NO document/Git handlers yet (step
// 1), so a message the core does not handle is an unknown op.
//
// This mirrors internal/ui.Manager's serve loop, stripped to the core surface: it
// merges uiws.CoreLevels (the authorization table) with no extra rows and passes an
// empty super-user-op set (the core contributes none — later Git/PR ops will add
// theirs). The shared Client.Gate enforces authz before each handler, exactly as in
// Shoka.
type wsManager struct {
	*uiws.CoreHandlers

	upgrader websocket.Upgrader
	logger   *slog.Logger
	connSeq  atomic.Uint64

	levels  map[uiws.MessageType]uiws.Op
	superOp map[uiws.MessageType]bool
}

func newWSManager(core *uiws.CoreHandlers, originAllowed func(*http.Request) bool, logger *slog.Logger) *wsManager {
	return &wsManager{
		CoreHandlers: core,
		upgrader: websocket.Upgrader{
			CheckOrigin: originAllowed,
		},
		logger:  logger,
		levels:  uiws.CoreLevels,
		superOp: map[uiws.MessageType]bool{},
	}
}

func (m *wsManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := m.upgrader.Upgrade(w, r, nil)
	if err != nil {
		m.logger.Warn("ws upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	// NewClient captures the WebUI session principal (attached by authapi.Middleware)
	// from the upgrade request, so the gate can read the connection's scope.
	client := uiws.NewClient(conn, fmt.Sprintf("ws-%d", m.connSeq.Add(1)), r)

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var wsMsg uiws.WSMessage
		if err := json.Unmarshal(message, &wsMsg); err != nil {
			client.SendError("Invalid message format")
			continue
		}

		// Single authorization choke point: every message is gated here, before its
		// handler, through the shared authz.Authorize. A refusal sends
		// PERMISSION_DENIED and skips dispatch.
		if !client.Gate(wsMsg.Type, wsMsg.Payload, m.levels, m.superOp) {
			continue
		}

		// Core ops (ACCOUNT_*/ADMIN_*/OAUTH_*/DOMAIN_*/CLIENT_*) are handled by the
		// embedded CoreHandlers; Dispatch returns true when it handled the message.
		if m.Dispatch(client, wsMsg.Type, wsMsg.Payload) {
			continue
		}

		// GitYard has no document/Git handlers yet (step 1): anything the core did not
		// claim is an unknown op.
		client.SendError(fmt.Sprintf("Unknown message type: %s", wsMsg.Type))
	}
}
