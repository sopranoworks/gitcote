package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/gorilla/websocket"
	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/gityard/internal/sshkeys"
	"github.com/sopranoworks/shoka/pkg/authz"
	"github.com/sopranoworks/shoka/pkg/uiws"
)

var NsLevels = map[uiws.MessageType]uiws.Op{
	MsgNamespaceHealth:  {Level: authz.LevelAdmin, Global: false},
	MsgCreateNamespace:  {Level: authz.LevelAdmin, Global: true},
	MsgDeleteNamespace:  {Level: authz.LevelAdmin, Global: true},
	MsgCreateProject:    {Level: authz.LevelAdmin, Global: false},
	MsgDeleteProject:    {Level: authz.LevelAdmin, Global: false},
	MsgRenameProject:    {Level: authz.LevelAdmin, Global: false},
	MsgRenameNamespace:  {Level: authz.LevelAdmin, Global: true},
	MsgMoveProject:      {Level: authz.LevelAdmin, Global: true},
	MsgNamespaceRecover: {Level: authz.LevelAdmin, Global: true},
}

type wsManager struct {
	*uiws.CoreHandlers

	upgrader websocket.Upgrader
	logger   *slog.Logger
	connSeq  atomic.Uint64

	levels   map[uiws.MessageType]uiws.Op
	superOp  map[uiws.MessageType]bool
	seedCtx        *seedContext
	gitStore       *git.Store
	sshKeyStore    *sshkeys.Store
	sshListenAddr  string
}

func newWSManager(core *uiws.CoreHandlers, originAllowed func(*http.Request) bool, sc *seedContext, gitStore *git.Store, sshKeyStore *sshkeys.Store, sshListenAddr string, logger *slog.Logger) *wsManager {
	levels := make(map[uiws.MessageType]uiws.Op, len(uiws.CoreLevels)+len(SeedLevels)+len(NsLevels)+len(ContentLevels)+len(UserSSHKeyLevels))
	for k, v := range uiws.CoreLevels {
		levels[k] = v
	}
	for k, v := range SeedLevels {
		levels[k] = v
	}
	for k, v := range NsLevels {
		levels[k] = v
	}
	for k, v := range ContentLevels {
		levels[k] = v
	}
	for k, v := range UserSSHKeyLevels {
		levels[k] = v
	}

	return &wsManager{
		CoreHandlers: core,
		upgrader: websocket.Upgrader{
			CheckOrigin: originAllowed,
		},
		logger:      logger,
		levels:      levels,
		superOp:     map[uiws.MessageType]bool{},
		seedCtx:       sc,
		gitStore:      gitStore,
		sshKeyStore:   sshKeyStore,
		sshListenAddr: sshListenAddr,
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

		if nsDispatch(client, m.gitStore, wsMsg.Type, wsMsg.Payload) {
			continue
		}

		if contentDispatch(client, m.gitStore, wsMsg.Type, wsMsg.Payload) {
			continue
		}

		if sshKeyDispatch(client, m.sshKeyStore, m.sshListenAddr, wsMsg.Type, wsMsg.Payload) {
			continue
		}

		client.SendError(fmt.Sprintf("Unknown message type: %s", wsMsg.Type))
	}
}
