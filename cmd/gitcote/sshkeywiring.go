package main

import (
	"encoding/json"
	"net"
	"strconv"

	"github.com/sopranoworks/gitcote/internal/sshkeys"
	"github.com/sopranoworks/shoka/pkg/authz"
	"github.com/sopranoworks/shoka/pkg/uiws"
)

const (
	MsgUserSSHKeyList   uiws.MessageType = "USER_SSH_KEY_LIST"
	MsgUserSSHKeyAdd    uiws.MessageType = "USER_SSH_KEY_ADD"
	MsgUserSSHKeyDelete uiws.MessageType = "USER_SSH_KEY_DELETE"
	MsgServerSSHInfo    uiws.MessageType = "SERVER_SSH_INFO"
)

var UserSSHKeyLevels = map[uiws.MessageType]uiws.Op{
	MsgUserSSHKeyList:   {Level: authz.LevelRead, Global: true},
	MsgUserSSHKeyAdd:    {Level: authz.LevelRead, Global: true},
	MsgUserSSHKeyDelete: {Level: authz.LevelRead, Global: true},
	MsgServerSSHInfo:    {Level: authz.LevelRead, Global: true},
}

func sshKeyDispatch(c *uiws.Client, keyStore *sshkeys.Store, sshListenAddr string, msgType uiws.MessageType, payload json.RawMessage) bool {
	switch msgType {
	case MsgUserSSHKeyList:
		handleUserSSHKeyList(c, keyStore)
	case MsgUserSSHKeyAdd:
		handleUserSSHKeyAdd(c, keyStore, payload)
	case MsgUserSSHKeyDelete:
		handleUserSSHKeyDelete(c, keyStore, payload)
	case MsgServerSSHInfo:
		handleServerSSHInfo(c, sshListenAddr)
	default:
		return false
	}
	return true
}

func handleUserSSHKeyList(c *uiws.Client, keyStore *sshkeys.Store) {
	principal := c.Principal()
	email := principal.Email
	if email == "" {
		c.SendError("no email on principal")
		return
	}
	keys, err := keyStore.ListByUser(email)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgUserSSHKeyList, map[string]any{"keys": keys})
}

type sshKeyAddPayload struct {
	PublicKey string `json:"publicKey"`
	Title     string `json:"title"`
}

func handleUserSSHKeyAdd(c *uiws.Client, keyStore *sshkeys.Store, payload json.RawMessage) {
	var p sshKeyAddPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	if p.PublicKey == "" {
		c.SendError("publicKey is required")
		return
	}
	principal := c.Principal()
	email := principal.Email
	if email == "" {
		c.SendError("no email on principal")
		return
	}
	fp, err := keyStore.Add(email, p.PublicKey, p.Title)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgUserSSHKeyAdd, map[string]string{"fingerprint": fp, "status": "ok"})
}

type sshKeyDeletePayload struct {
	Fingerprint string `json:"fingerprint"`
}

func handleUserSSHKeyDelete(c *uiws.Client, keyStore *sshkeys.Store, payload json.RawMessage) {
	var p sshKeyDeletePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	principal := c.Principal()
	email := principal.Email
	if email == "" {
		c.SendError("no email on principal")
		return
	}
	if err := keyStore.Delete(email, p.Fingerprint); err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgUserSSHKeyDelete, map[string]string{"status": "ok"})
}

func handleServerSSHInfo(c *uiws.Client, listenAddr string) {
	if listenAddr == "" {
		c.SendResponse(MsgServerSSHInfo, map[string]any{"enabled": false})
		return
	}
	_, portStr, err := net.SplitHostPort(listenAddr)
	if err != nil {
		c.SendResponse(MsgServerSSHInfo, map[string]any{"enabled": false})
		return
	}
	port, _ := strconv.Atoi(portStr)
	c.SendResponse(MsgServerSSHInfo, map[string]any{"enabled": true, "port": port})
}
