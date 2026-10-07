package worker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Self-Command/paca-plugin-task-checkin/internal/buildinfo"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const PluginID = "com.selfcommand.task-checkin"
const Schema = "plugin_data_com_selfcommand_task_checkin"

type Worker struct {
	DB                                                          *pgxpool.Pool
	API, Key, Secret, Public, GrantSecret, Bucket, ActionSecret string
	HTTP                                                        *http.Client
	Objects                                                     *minio.Client
	Decode                                                      chan struct{}
}
type apiError struct{ Code int }

func (e apiError) Error() string { return fmt.Sprintf("Paca API HTTP %d", e.Code) }
func secret(name string) (string, error) {
	p := os.Getenv(name + "_FILE")
	if p == "" {
		return "", fmt.Errorf("%s_FILE required", name)
	}
	b, e := os.ReadFile(p)
	return strings.TrimSpace(string(b)), e
}
func New(ctx context.Context) (*Worker, error) {
	w := &Worker{API: strings.TrimRight(os.Getenv("PACA_API_URL"), "/"), Public: strings.TrimRight(os.Getenv("PUBLIC_URL"), "/"), Bucket: os.Getenv("CHECKIN_BUCKET"), Decode: make(chan struct{}, 1), HTTP: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	var err error
	if os.Getenv("ACTION_SECRET_FILE") != "" {
		if w.ActionSecret, err = secret("ACTION_SECRET"); err != nil {
			return nil, err
		}
	}
	if w.Key, err = secret("PACA_API_KEY"); err != nil {
		return nil, err
	}
	if w.Secret, err = secret("WORKER_SECRET"); err != nil {
		return nil, err
	}
	if w.GrantSecret, err = secret("GRANT_SECRET"); err != nil {
		return nil, err
	}
	if len(w.Secret) != 64 || len(w.GrantSecret) < 64 {
		return nil, errors.New("worker and grant secrets must be strong")
	}
	public, err := url.Parse(w.Public)
	if err != nil || public.Host == "" || public.User != nil || public.RawQuery != "" || public.Fragment != "" || public.Path != "" {
		return nil, errors.New("valid fixed PUBLIC_URL required")
	}
	if public.Scheme != "https" && !(os.Getenv("CHECKIN_TEST_MODE") == "true" && public.Scheme == "http" && (public.Hostname() == "127.0.0.1" || public.Hostname() == "localhost")) {
		return nil, errors.New("PUBLIC_URL must use HTTPS")
	}
	api, err := url.Parse(w.API)
	if err != nil || api.Host == "" || api.User != nil || (api.Scheme != "http" && api.Scheme != "https") {
		return nil, errors.New("fixed PACA_API_URL required")
	}
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = Schema
	cfg.MaxConns = 5
	if w.DB, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		return nil, err
	}
	access, err := secret("STORAGE_ACCESS_KEY")
	if err != nil {
		return nil, err
	}
	key, err := secret("STORAGE_SECRET_KEY")
	if err != nil {
		return nil, err
	}
	endpoint, err := url.Parse(os.Getenv("CHECKIN_S3_ENDPOINT"))
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, errors.New("fixed S3 endpoint required")
	}
	if w.Bucket == "" {
		return nil, errors.New("private CHECKIN_BUCKET required")
	}
	w.Objects, err = minio.New(endpoint.Host, &minio.Options{Creds: credentials.NewStaticV4(access, key, ""), Secure: endpoint.Scheme == "https", Region: "us-east-1"})
	return w, err
}
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func tokenHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func (w *Worker) control(ctx context.Context) error {
	nonce, err := randomToken()
	if err != nil {
		return err
	}
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(w.Secret))
	mac.Write([]byte("GET\n/worker/control\n" + stamp + "\n" + nonce))
	req, err := http.NewRequestWithContext(ctx, "GET", w.API+"/api/v1/plugins/"+PluginID+"/worker/control", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Worker-Timestamp", stamp)
	req.Header.Set("X-Worker-Nonce", nonce)
	req.Header.Set("X-Worker-Signature", hex.EncodeToString(mac.Sum(nil)))
	response, err := w.HTTP.Do(req)
	if err != nil {
		return errors.New("host control unavailable")
	}
	defer response.Body.Close()
	var c struct {
		Error string `json:"error"`
		ID      string `json:"id"`
		Version string `json:"version"`
		Source  string `json:"source_sha"`
		Enabled bool   `json:"enabled"`
		Schema  int    `json:"schema_version"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&c)
	if response.StatusCode != 200 || decodeErr != nil || !c.Enabled || c.ID != PluginID || c.Schema != 1 || c.Version != buildinfo.Version || c.Source != buildinfo.SourceSHA {
		return fmt.Errorf("host control HTTP %d: enabled=%t id=%s schema=%d version=%s source=%s reason=%.200s", response.StatusCode, c.Enabled, c.ID, c.Schema, c.Version, c.Source, c.Error)
	}
	return nil
}
func (w *Worker) call(ctx context.Context, method, path string, body, out any) error {
	if err := w.control(ctx); err != nil {
		return err
	}
	var input io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		input = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, w.API+"/api/v1"+path, input)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", w.Key)
	req.Header.Set("Content-Type", "application/json")
	response, err := w.HTTP.Do(req)
	if err != nil {
		return errors.New("Paca API response unknown")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return apiError{response.StatusCode}
	}
	if out == nil {
		return nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&envelope); err != nil {
		return err
	}
	// Plugin SDK responses are unwrapped; core responses use data.
	if len(envelope.Data) == 0 {
		return errors.New("expected core API envelope")
	}
	return json.Unmarshal(envelope.Data, out)
}
func (w *Worker) source(ctx context.Context, project, task string) (json.RawMessage, error) {
	if err := w.control(ctx); err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", w.API+"/api/v1/plugins/com.selfcommand.tasknotes-webhook/projects/"+project+"/tasks/"+task+"/source-link", nil)
	req.Header.Set("X-API-Key", w.Key)
	response, err := w.HTTP.Do(req)
	if err != nil {
		return nil, errors.New("source lookup unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, apiError{response.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if !json.Valid(raw) {
		return nil, errors.New("invalid source reply")
	}
	return raw, nil
}
