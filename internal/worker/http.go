package worker

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Self-Command/paca-plugin-task-checkin/internal/model"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type session struct {
	Grant, Kind string
	Instance    model.Instance
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	code := map[int]string{400: "invalid_request", 401: "token", 403: "token", 404: "missing", 409: "revision", 410: "expired", 503: "unavailable"}[status]
	writeJSON(w, status, map[string]any{"error": message, "code": code, "retryable": status == 503, "request_id": w.Header().Get("X-Request-ID")})
}
func readJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil {
		fail(w, 400, "invalid JSON request")
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		fail(w, 400, "extra JSON data")
		return false
	}
	return true
}
func (w *Worker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(out http.ResponseWriter, r *http.Request) {
		if err := w.control(r.Context()); err != nil {
			fail(out, 503, "host unavailable or plugin disabled")
			return
		}
		writeJSON(out, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /internal/v1/pairing-info", w.pairingInfo)
	mux.HandleFunc("GET /internal/v1/sync/info", w.delegated(w.syncInfo))
	mux.HandleFunc("GET /internal/v1/sync/changes", w.delegated(w.changes))
	mux.HandleFunc("GET /internal/v1/sync/sources/{task}", w.delegated(w.syncSource))
	mux.HandleFunc("GET /internal/v1/sync/media/{id}", w.delegated(w.syncMedia))
	mux.HandleFunc("POST /internal/v1/sync/receipts", w.delegated(w.prepareReceipt))
	mux.HandleFunc("POST /internal/v1/sync/receipts/{id}/ack", w.delegated(w.ackReceipt))
	mux.HandleFunc("POST /internal/v1/sync/conflicts", w.delegated(w.saveConflict))
	mux.HandleFunc("POST /internal/v1/sync/conflicts/{id}/resolve", w.delegated(w.resolveConflict))
	mux.HandleFunc("GET /internal/v1/sync/deliveries", w.delegated(w.deliveries))
	mux.HandleFunc("POST /internal/v1/sync/deliveries/report", w.delegated(w.reportDelivery))
	mux.HandleFunc("POST /internal/v1/sync/deliveries/actions", w.delegated(w.deliveryAction))
	mux.HandleFunc("POST /internal/v1/action", w.action)
	mux.HandleFunc("POST /internal/v1/task", w.taskPlan)
	mux.HandleFunc("POST /internal/v1/times/freeze", w.timeFreeze)
	mux.HandleFunc("POST /internal/v1/writeback-match", w.matchWriteback)
	mux.HandleFunc("POST /checkin-api/v1/exchange", w.exchange)
	mux.HandleFunc("GET /checkin-api/v1/session", w.view)
	mux.HandleFunc("POST /checkin-api/v1/photos", w.upload)
	mux.HandleFunc("POST /checkin-api/v1/submit", w.submit)
	mux.HandleFunc("GET /checkin-api/v1/photo/{id}", w.photo)
	mux.HandleFunc("GET /checkin-api/v1/sync/info", w.syncInfo)
	mux.HandleFunc("GET /checkin-api/v1/sync/sources/{task}", w.syncSource)
	mux.HandleFunc("GET /checkin-api/v1/sync/changes", w.changes)
	mux.HandleFunc("GET /checkin-api/v1/sync/media/{id}", w.syncMedia)
	mux.HandleFunc("POST /checkin-api/v1/sync/receipts", w.prepareReceipt)
	mux.HandleFunc("POST /checkin-api/v1/sync/receipts/{id}/ack", w.ackReceipt)
	mux.HandleFunc("POST /checkin-api/v1/sync/conflicts", w.saveConflict)
	mux.HandleFunc("POST /checkin-api/v1/sync/conflicts/{id}/resolve", w.resolveConflict)
	mux.HandleFunc("GET /checkin-api/v1/sync/deliveries", w.deliveries)
	mux.HandleFunc("POST /checkin-api/v1/sync/deliveries/report", w.reportDelivery)
	mux.HandleFunc("POST /checkin-api/v1/sync/deliveries/actions", w.deliveryAction)
	assets := os.Getenv("CHECKIN_WEB_DIR")
	if assets == "" {
		assets = "/web"
	}
	mux.HandleFunc("GET /checkin/", func(out http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/checkin/")
		if strings.Contains(path, "..") || strings.Contains(path, "\\") {
			http.NotFound(out, r)
			return
		}
		target := filepath.Join(assets, path)
		if info, err := os.Stat(target); err != nil || info.IsDir() {
			target = filepath.Join(assets, "checkin.html")
		}
		out.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		out.Header().Set("Cache-Control", "no-store")
		http.ServeFile(out, r, target)
	})
	return http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		out.Header().Set("X-Content-Type-Options", "nosniff")
		out.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != "GET" && r.Method != "HEAD" && strings.HasPrefix(r.URL.Path, "/checkin-api/") && !strings.Contains(r.URL.Path, "/sync/") {
			if r.Header.Get("Origin") != w.Public {
				fail(out, 403, "same-origin browser request required")
				return
			}
		}
		mux.ServeHTTP(out, r)
	})
}
func (w *Worker) action(out http.ResponseWriter, r *http.Request) {
	expected := "Bearer " + w.ActionSecret
	if w.ActionSecret == "" || !hmac.Equal([]byte(r.Header.Get("Authorization")), []byte(expected)) {
		fail(out, 401, "internal binding authentication required")
		return
	}
	var input struct {
		Project string    `json:"project_id"`
		Task    string    `json:"task_id"`
		Kind    string    `json:"kind"`
		Target  time.Time `json:"target"`
	}
	if !readJSON(out, r, &input) {
		return
	}
	if !model.UUID.MatchString(input.Project) || !model.UUID.MatchString(input.Task) {
		fail(out, 400, "invalid task identity")
		return
	}
	if err := w.control(r.Context()); err != nil {
		fail(out, 503, "host unavailable")
		return
	}
	// A normal creation notification is not a check-in card.
	if input.Kind != "start" && input.Kind != "due" {
		writeJSON(out, 200, map[string]any{"enabled": false})
		return
	}
	cfg, err := w.config(r.Context(), input.Project)
	if err != nil || !cfg.Enabled {
		fail(out, 409, "check-in project disabled or not configured")
		return
	}
	i, err := w.prepare(r.Context(), input.Project, input.Task)
	if err != nil {
		fail(out, 409, err.Error())
		return
	}
	_, close, err := i.Window(input.Kind)
	if err != nil || !close.Equal(input.Target) {
		fail(out, 409, "reminder and check-in target differ")
		return
	}
	id, err := randomID()
	if err != nil {
		fail(out, 503, "randomness unavailable")
		return
	}
	mac := hmac.New(sha256.New, []byte(w.GrantSecret))
	mac.Write([]byte("v1\n" + i.ID + "\n" + input.Kind))
	token := hex.EncodeToString(mac.Sum(nil))
	_, err = w.DB.Exec(r.Context(), "INSERT INTO grants(id,instance_id,kind,token_hash,expires_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(instance_id,kind) DO NOTHING", id, i.ID, input.Kind, tokenHash(token), close.Add(5*time.Minute))
	if err != nil {
		fail(out, 503, "grant save failed")
		return
	}
	card, cardErr := w.taskCard(r.Context(), i, cfg)
	if cardErr != nil {
		fail(out, 503, "task card unavailable")
		return
	}
	cardJSON, _ := json.Marshal(card)
	writeJSON(out, 200, map[string]any{"enabled": true, "instance_id": i.ID, "instance_revision": i.Revision, "expires_at": close, "metadata": map[string]string{"action_version": "1", "action_kind": "web", "action_label": "去打卡", "action_url": w.Public + "/checkin/" + i.ID + "/" + input.Kind + "#token=" + token, "task_card_version": "1", "task_card": string(cardJSON)}})
}
func (w *Worker) exchange(out http.ResponseWriter, r *http.Request) {
	if err := w.control(r.Context()); err != nil {
		fail(out, 503, "plugin unavailable")
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if !readJSON(out, r, &input) {
		return
	}
	if len(input.Token) != 64 {
		fail(out, 401, "invalid link")
		return
	}
	var grant, instance, kind string
	var close time.Time
	err := w.DB.QueryRow(r.Context(), "SELECT g.id::text,g.expires_at,g.instance_id::text,g.kind FROM grants g JOIN instances i ON i.id=g.instance_id JOIN project_settings p ON p.project_id=i.project_id WHERE g.token_hash=$1 AND g.expires_at>clock_timestamp() AND i.state IN('active','frozen') AND p.config->>'enabled'='true'", tokenHash(input.Token)).Scan(&grant, &close, &instance, &kind)
	if err != nil {
		fail(out, 410, "link expired, cancelled or replaced")
		return
	}
	token, err := randomToken()
	if err != nil {
		fail(out, 503, "randomness unavailable")
		return
	}
	expires := time.Now().Add(30 * time.Minute)
	if close.Before(expires) {
		expires = close
	}
	_, err = w.DB.Exec(r.Context(), "INSERT INTO sessions(token_hash,grant_id,expires_at) VALUES($1,$2,$3)", tokenHash(token), grant, expires)
	if err != nil {
		fail(out, 503, "session persistence failed")
		return
	}
	http.SetCookie(out, &http.Cookie{Name: "paca_checkin_" + instance + "_" + kind, Value: token, Path: "/checkin-api/v1/", Secure: strings.HasPrefix(w.Public, "https://"), HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expires})
	writeJSON(out, 200, map[string]any{"ok": true})
}
func (w *Worker) session(ctx context.Context, r *http.Request) (session, error) {
	if err := w.control(ctx); err != nil {
		return session{}, err
	}
	expectedInstance := r.Header.Get("X-Checkin-Instance")
	expectedKind := r.Header.Get("X-Checkin-Kind")
	if !model.UUID.MatchString(expectedInstance) || (expectedKind != "start" && expectedKind != "due") {
		return session{}, errors.New("explicit card context required")
	}
	cookie, err := r.Cookie("paca_checkin_" + expectedInstance + "_" + expectedKind)
	if err != nil {
		return session{}, err
	}
	var s session
	var id string
	err = w.DB.QueryRow(ctx, "SELECT g.id::text,g.kind,g.instance_id::text FROM sessions s JOIN grants g ON g.id=s.grant_id WHERE s.token_hash=$1 AND s.expires_at>clock_timestamp() AND g.expires_at>clock_timestamp()", tokenHash(cookie.Value)).Scan(&s.Grant, &s.Kind, &id)
	if err != nil {
		return s, err
	}
	s.Instance, err = scanInstance(w.DB.QueryRow(ctx, "SELECT "+instanceColumns+" FROM instances WHERE id=$1", id))
	if err != nil {
		return s, err
	}
	if s.Instance.ID != expectedInstance || s.Kind != expectedKind {
		return s, errors.New("wrong card context")
	}
	if s.Instance.State != "active" && s.Instance.State != "frozen" {
		return s, errors.New("instance unavailable")
	}
	if _, err = w.config(ctx, s.Instance.Project); err != nil {
		return s, err
	}
	return s, nil
}
func prepareStatus(err error) int {
	if err == nil {
		return 410
	}
	var api apiError
	if errors.As(err, &api) {
		if api.Code == 404 {
			return 410
		}
		return 503
	}
	text := err.Error()
	if strings.Contains(text, "not eligible") || strings.Contains(text, "cancel") || strings.Contains(text, "time frozen") {
		return 410
	}
	if strings.Contains(text, "reconfirm") || strings.Contains(text, "conflict") {
		return 409
	}
	return 503
}
func (w *Worker) view(out http.ResponseWriter, r *http.Request) {
	s, err := w.session(r.Context(), r)
	if err != nil {
		fail(out, 410, "link expired or unavailable")
		return
	}
	i, err := w.prepare(r.Context(), s.Instance.Project, s.Instance.Task)
	if err != nil || i.ID != s.Instance.ID {
		fail(out, prepareStatus(err), "task cancelled, changed or temporarily unavailable")
		return
	}
	var now time.Time
	_ = w.DB.QueryRow(r.Context(), "SELECT clock_timestamp()").Scan(&now)
	cfg, configErr := w.config(r.Context(), i.Project)
	if configErr != nil {
		fail(out, 503, "project settings unavailable")
		return
	}
	open, close, _ := i.Window(s.Kind)
	var record json.RawMessage
	err = w.DB.QueryRow(r.Context(), "SELECT jsonb_build_object('id',id,'media_id',media_id,'note',note,'submitted_at',submitted_at,'status',status) FROM records WHERE instance_id=$1 AND kind=$2", i.ID, s.Kind).Scan(&record)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		fail(out, 503, "record unavailable")
		return
	}
	card, cardErr := w.taskCard(r.Context(), i, cfg)
	if cardErr != nil {
		fail(out, 503, "task details temporarily unavailable")
		return
	}
	writeJSON(out, 200, map[string]any{"instance_id": i.ID, "revision": i.Revision, "title": i.Title, "kind": s.Kind, "timezone": cfg.Timezone, "task": card, "opens_at": open, "closes_at": close, "server_time": now, "can_submit": i.CanSubmit(s.Kind, now) == nil && len(record) == 0, "record": record})
}
