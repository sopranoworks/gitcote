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

type wsManager struct {
	*uiws.CoreHandlers

	upgrader websocket.Upgrader
	logger   *slog.Logger
	connSeq  atomic.Uint64

	levels  map[uiws.MessageType]uiws.Op
	superOp map[uiws.MessageType]bool
	seedCtx *seedContext
}

func newWSManager(core *uiws.CoreHandlers, originAllowed func(*http.Request) bool, sc *seedContext, logger *slog.Logger) *wsManager {
	levels := make(map[uiws.MessageType]uiws.Op, len(uiws.CoreLevels)+len(SeedLevels))
	for k, v := range uiws.CoreLevels {
		levels[k] = v
	}
	for k, v := range SeedLevels {
		levels[k] = v
	}

	return &wsManager{
		CoreHandlers: core,
		upgrader: websocket.Upgrader{
			CheckOrigin: originAllowed,
		},
		logger:  logger,
		levels:  levels,
		superOp: map[uiws.MessageType]bool{},
		seedCtx: sc,
	}
}

func (m *wsManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := m.upgrader.Upgrade(w, r, nil)
	if err != nil {
		m.logger.Warn("ws upgrade failed", "error", err)
		return
	}
	defer conn.Close()

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

		if !client.Gate(wsMsg.Type, wsMsg.Payload, m.levels, m.superOp) {
			continue
		}

		if m.Dispatch(client, wsMsg.Type, wsMsg.Payload) {
			continue
		}

		if seedDispatch(client, m.seedCtx, wsMsg.Type, wsMsg.Payload) {
			continue
		}

		client.SendError(fmt.Sprintf("Unknown message type: %s", wsMsg.Type))
	}
}
