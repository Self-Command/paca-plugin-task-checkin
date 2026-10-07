package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	plugin "github.com/Paca-AI/plugin-sdk-go"
	"github.com/Self-Command/paca-plugin-task-checkin/internal/buildinfo"
	"strconv"
	"strings"
	"time"
)

func (p *integrationPlugin) rotateWorkerCredential(req *plugin.Request, res *plugin.Response) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		res.Error(500, "randomness unavailable")
		return
	}
	plain := hex.EncodeToString(secret)
	encrypted, err := p.encrypt(plain)
	if err != nil {
		res.Error(503, "valid ENCRYPTION_KEY required")
		return
	}
	if _, err = p.db.Exec("INSERT INTO worker_settings(id,secret_enc) VALUES(1,$1) ON CONFLICT(id) DO UPDATE SET secret_enc=EXCLUDED.secret_enc, revision=worker_settings.revision+1, updated_at=NOW()", encrypted); err != nil {
		res.Error(503, "credential persistence failed")
		return
	}
	res.JSON(201, map[string]any{"secret": plain, "version": pluginVersion, "warning": "Save once in the worker secret file; subsequent queries never return it."})
}

func validWorkerSignature(secret, timestamp, nonce, signature string, now time.Time) bool {
	unix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || len(nonce) < 16 || len(nonce) > 128 || unix < now.Unix()-30 || unix > now.Unix()+30 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("GET\n/worker/control\n" + timestamp + "\n" + nonce))
	received, err := hex.DecodeString(signature)
	return err == nil && hmac.Equal(received, mac.Sum(nil))
}

func (p *integrationPlugin) workerControl(req *plugin.Request, res *plugin.Response) {
	row, err := p.db.Query("SELECT secret_enc,enabled,revision FROM worker_settings WHERE id=1")
	if err != nil || len(row.Rows) != 1 {
		res.Error(503, "worker not configured")
		return
	}
	cipher, ok := row.Rows[0][0].(string)
	if !ok {
		res.Error(503, "credential unavailable")
		return
	}
	secret, err := p.decrypt(cipher)
	if err != nil {
		res.Error(503, "credential unavailable")
		return
	}
	nonce := requestHeader(req, "x-worker-nonce")
	if !validWorkerSignature(secret, requestHeader(req, "x-worker-timestamp"), nonce, requestHeader(req, "x-worker-signature"), time.Now()) {
		res.Error(401, "invalid worker signature")
		return
	}
	if _, err = p.db.Exec("DELETE FROM worker_nonces WHERE created_at < NOW()-INTERVAL '2 minutes'"); err != nil {
		res.Error(503, "worker authorization unavailable")
		return
	}
	changed, err := p.db.Exec("INSERT INTO worker_nonces(nonce) VALUES($1) ON CONFLICT(nonce) DO NOTHING", nonce)
	if err != nil {
		res.Error(503, "worker authorization unavailable")
		return
	}
	if changed != 1 {
		res.Error(409, "worker request already used")
		return
	}
	res.JSON(200, map[string]any{"id": pluginID, "version": pluginVersion, "source_sha": buildinfo.SourceSHA, "schema_version": 1, "enabled": row.Rows[0][1], "revision": row.Rows[0][2]})
}

func requestHeader(req *plugin.Request, name string) string {
	for key, value := range req.Headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}
