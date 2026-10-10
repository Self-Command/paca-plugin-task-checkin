package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/Self-Command/paca-plugin-task-checkin/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"log"
	"strconv"
	"net/http"
	"time"
)

func normalizePhoto(raw []byte) ([]byte, error) {
	cfg, kind, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || (kind != "jpeg" && kind != "png" && kind != "webp") {
		return nil, errors.New("only valid JPEG, PNG and WebP images are supported")
	}
	if cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 25000000 {
		return nil, errors.New("image exceeds 25 megapixels")
	}
	decoded, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("image decode failed")
	}
	target := decoded
	width, height := cfg.Width, cfg.Height
	if width > 2048 || height > 2048 {
		if width >= height {
			height = height * 2048 / width
			width = 2048
		} else {
			width = width * 2048 / height
			height = 2048
		}
		if width < 1 {
			width = 1
		}
		if height < 1 {
			height = 1
		}
		scaled := image.NewRGBA(image.Rect(0, 0, width, height))
		draw.CatmullRom.Scale(scaled, scaled.Bounds(), decoded, decoded.Bounds(), draw.Src, nil)
		target = scaled
	}
	var result bytes.Buffer
	if err = jpeg.Encode(&result, target, &jpeg.Options{Quality: 90}); err != nil {
		return nil, err
	}
	return result.Bytes(), nil
}
func (w *Worker) upload(out http.ResponseWriter, r *http.Request) {
	s, err := w.session(r.Context(), r)
	if err != nil {
		fail(out, 401, "valid check-in session required")
		return
	}
	i, err := w.prepare(r.Context(), s.Instance.Project, s.Instance.Task)
	if err != nil || i.ID != s.Instance.ID {
		fail(out, prepareStatus(err), "instance changed, cancelled or temporarily unavailable")
		return
	}
	var now time.Time
	_ = w.DB.QueryRow(r.Context(), "SELECT clock_timestamp()").Scan(&now)
	if err = i.CanSubmit(s.Kind, now); err != nil {
		fail(out, 410, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(out, r.Body, 10<<20)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		fail(out, 413, "photo exceeds 10 MiB")
		return
	}
	select {
	case w.Decode <- struct{}{}:
		defer func() { <-w.Decode }()
	default:
		fail(out, 429, "image processing busy; retry shortly")
		return
	}
	photo, err := normalizePhoto(raw)
	if err != nil {
		fail(out, 400, err.Error())
		return
	}
	id, err := randomID()
	if err != nil {
		fail(out, 503, "randomness unavailable")
		return
	}
	object := "checkin/" + i.Project + "/" + i.ID + "/" + id + ".jpg"
	// Store cleanup intent before uploading. A crash cannot orphan an untracked object.
	_, err = w.DB.Exec(r.Context(), "INSERT INTO media(id,instance_id,kind,object_key,sha256,bytes,mime,state) VALUES($1,$2,$3,$4,$5,$6,'image/jpeg','uploading')", id, i.ID, s.Kind, object, tokenHash(string(photo)), len(photo))
	if err != nil {
		fail(out, 503, "photo upload intent failed")
		return
	}
	_, err = w.Objects.PutObject(r.Context(), w.Bucket, object, bytes.NewReader(photo), int64(len(photo)), minio.PutObjectOptions{ContentType: "image/jpeg"})
	if err != nil {
		fail(out, 503, "photo storage unavailable")
		return
	}
	if _, err = w.DB.Exec(r.Context(), "UPDATE media SET state='temporary' WHERE id=$1 AND state='uploading'", id); err != nil {
		fail(out, 503, "photo receipt save failed")
		return
	}
	writeJSON(out, 201, map[string]any{"media_id": id, "sha256": tokenHash(string(photo)), "bytes": len(photo), "mime": "image/jpeg"})
}
func (w *Worker) submit(out http.ResponseWriter, r *http.Request) {
	s, err := w.session(r.Context(), r)
	if err != nil {
		fail(out, 401, "valid check-in session required")
		return
	}
	var input struct {
		Media    string `json:"media_id"`
		Note     string `json:"note"`
		Op       string `json:"op_id"`
		Revision int    `json:"revision"`
	}
	if !readJSON(out, r, &input) {
		return
	}
	if !model.UUID.MatchString(input.Media) || len(input.Note) > 4000 || len(input.Op) < 8 || len(input.Op) > 128 {
		fail(out, 400, "invalid media, note or operation ID")
		return
	}
	i, err := w.prepare(r.Context(), s.Instance.Project, s.Instance.Task)
	if err != nil || i.ID != s.Instance.ID {
		fail(out, prepareStatus(err), "task changed or temporarily unavailable")
		return
	}
	cfg, err := w.config(r.Context(), i.Project)
	if err != nil {
		fail(out, 409, "check-in disabled")
		return
	}
	tx, err := w.DB.Begin(r.Context())
	if err != nil {
		fail(out, 503, "database unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	i, err = scanInstance(tx.QueryRow(r.Context(), "SELECT "+instanceColumns+" FROM instances WHERE id=$1 FOR UPDATE", i.ID))
	if err != nil {
		fail(out, 503, "instance unavailable")
		return
	}
	var existing, op, media, note string
	err = tx.QueryRow(r.Context(), "SELECT id::text,op_id,media_id::text,note FROM records WHERE instance_id=$1 AND kind=$2", i.ID, s.Kind).Scan(&existing, &op, &media, &note)
	if err == nil {
		if op != input.Op || media != input.Media || note != input.Note {
			fail(out, 409, "card already submitted; original record retained")
			return
		}
		writeJSON(out, 200, map[string]any{"id": existing, "duplicate": true})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		fail(out, 503, "record check failed")
		return
	}
	if input.Revision != i.Revision {
		fail(out, 409, "instance revision changed")
		return
	}
	var hasEnd bool
	if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM records WHERE instance_id=$1 AND kind='due')", i.ID).Scan(&hasEnd); err != nil {
		fail(out, 503, "card check failed")
		return
	}
	desired := model.DesiredStatus(s.Kind, hasEnd, cfg)
	var sha string
	var bytes int64
	err = tx.QueryRow(r.Context(), "SELECT sha256,bytes FROM media WHERE id=$1 AND instance_id=$2 AND kind=$3 AND state='temporary' FOR UPDATE", input.Media, i.ID, s.Kind).Scan(&sha, &bytes)
	if err != nil {
		fail(out, 400, "verified photo for this card required")
		return
	}
	id, err := randomID()
	if err != nil {
		fail(out, 503, "randomness unavailable")
		return
	}
	// This timestamp is read after locks and media validation, not at transaction start.
	var submitted time.Time
	if err = tx.QueryRow(r.Context(), "SELECT clock_timestamp()").Scan(&submitted); err != nil {
		fail(out, 503, "clock unavailable")
		return
	}
	if err = i.CanSubmit(s.Kind, submitted); err != nil {
		fail(out, 410, err.Error())
		return
	}
	_, err = tx.Exec(r.Context(), "INSERT INTO records(id,instance_id,kind,media_id,note,submitted_at,status,op_id) VALUES($1,$2,$3,$4,$5,$6,'pending_sync',$7)", id, i.ID, s.Kind, input.Media, input.Note, submitted, input.Op)
	if err != nil {
		fail(out, 409, "submission already exists or conflicts")
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE media SET state='committed' WHERE id=$1", input.Media); err != nil {
		fail(out, 503, "photo commit failed")
		return
	}
	revision := i.StatusRevision + 1
	if _, err = tx.Exec(r.Context(), "UPDATE instances SET status_revision=$2,state='frozen' WHERE id=$1", i.ID, revision); err != nil {
		fail(out, 503, "revision save failed")
		return
	}
	if _, err = tx.Exec(r.Context(), "INSERT INTO outbox(instance_id,revision,status_id) VALUES($1,$2,$3)", i.ID, revision, desired); err != nil {
		fail(out, 503, "status outbox failed")
		return
	}
	var source map[string]any
	_ = json.Unmarshal(i.Source, &source)
	connection, _ := source["connection_id"].(string)
	logical := "started"
	if s.Kind == "due" || hasEnd {
		logical = "completed"
	}
	payload := map[string]any{"record_id": id, "instance_id": i.ID, "task_id": i.Task, "revision": revision, "logical_status": logical, "paca_status_id": desired, "kind": s.Kind, "submitted_at": submitted, "note": input.Note, "media_id": input.Media, "media_sha256": sha, "media_bytes": bytes, "media_mime": "image/jpeg", "source": source}
	raw, _ := json.Marshal(payload)
	if _, err = tx.Exec(r.Context(), "INSERT INTO changes(project_id,connection_id,instance_id,record_id,revision,payload) VALUES($1,$2,$3,$4,$5,$6::jsonb)", i.Project, connection, i.ID, id, revision, string(raw)); err != nil {
		fail(out, 503, "sync feed save failed")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		fail(out, 503, "commit response unknown; retry same operation")
		return
	}
	writeJSON(out, 201, map[string]any{"id": id, "status": "pending_sync", "submitted_at": submitted})
}
func (w *Worker) photo(out http.ResponseWriter, r *http.Request) {
	s, err := w.session(r.Context(), r)
	if err != nil {
		fail(out, 401, "valid session required")
		return
	}
	var object string
	err = w.DB.QueryRow(r.Context(), "SELECT m.object_key FROM media m JOIN records r ON r.media_id=m.id WHERE m.id=$1 AND m.instance_id=$2 AND r.kind=$3 AND m.state='committed'", r.PathValue("id"), s.Instance.ID, s.Kind).Scan(&object)
	if err != nil {
		fail(out, 404, "photo unavailable or expired")
		return
	}
	w.streamPhoto(out, r, object)
}
func (w *Worker) streamPhoto(out http.ResponseWriter, r *http.Request, object string) {
	photo, err := w.Objects.GetObject(r.Context(), w.Bucket, object, minio.GetObjectOptions{})
	if err != nil {
		fail(out, 503, "photo storage unavailable")
		return
	}
	defer photo.Close()
	info, err := photo.Stat()
	if err != nil {
		fail(out, 404, "photo expired or unavailable")
		return
	}
	out.Header().Set("Content-Type", "image/jpeg")
	out.Header().Set("Cache-Control", "private, no-store")
	out.Header().Set("Content-Disposition", "inline; filename=checkin.jpg")
	out.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	out.Header().Set("X-Accel-Buffering", "no")
	_ = http.NewResponseController(out).SetWriteDeadline(time.Now().Add(300 * time.Second))
	n, copyErr := io.Copy(out, photo)
	if copyErr != nil || n != info.Size {
		log.Printf("photo_transfer incomplete bytes=%d expected=%d", n, info.Size)
		panic(http.ErrAbortHandler)
	}
}
