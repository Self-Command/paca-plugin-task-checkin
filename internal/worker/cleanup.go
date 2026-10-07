package worker

import (
	"context"
	"github.com/minio/minio-go/v7"
)

func (w *Worker) Cleanup(ctx context.Context) error {
	if err := w.control(ctx); err != nil {
		return err
	}
	rows, err := w.DB.Query(ctx, "SELECT m.id::text,m.object_key FROM media m JOIN instances i ON i.id=m.instance_id JOIN project_settings p ON p.project_id=i.project_id WHERE (m.state='temporary' AND m.created_at<NOW()-INTERVAL '24 hours') OR (m.state='committed' AND m.created_at<NOW()-((p.config->>'retention_days')::integer*INTERVAL '1 day')) LIMIT 20")
	if err != nil {
		return err
	}
	type item struct{ id, key string }
	items := []item{}
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.id, &i.key); err != nil {
			rows.Close()
			return err
		}
		items = append(items, i)
	}
	rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}
	for _, i := range items {
		if err = w.Objects.RemoveObject(ctx, w.Bucket, i.key, minio.RemoveObjectOptions{}); err != nil {
			return err
		}
		if _, err = w.DB.Exec(ctx, "UPDATE media SET state='expired' WHERE id=$1", i.id); err != nil {
			return err
		}
	}
	_, err = w.DB.Exec(ctx, "DELETE FROM sessions WHERE expires_at<NOW()")
	return err
}
