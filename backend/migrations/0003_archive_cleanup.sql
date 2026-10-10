-- Keep only scoped deletion markers after the associated record is removed.
ALTER TABLE sync_delivery_events DROP CONSTRAINT IF EXISTS sync_delivery_events_record_id_fkey;
ALTER TABLE sync_delivery_ops DROP CONSTRAINT IF EXISTS sync_delivery_ops_record_id_fkey;
CREATE TABLE sync_purges(
 project_id UUID NOT NULL,
 connection_id TEXT NOT NULL,
 record_id UUID NOT NULL,
 instance_id UUID NOT NULL,
 media_id UUID,
 object_key TEXT NOT NULL,
 op_id TEXT NOT NULL,
 revision BIGINT NOT NULL,
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN('pending','failed','complete')),
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 last_error TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 cleaned_at TIMESTAMPTZ,
 PRIMARY KEY(connection_id,record_id)
);
CREATE INDEX sync_purges_ready ON sync_purges(state,next_attempt);
UPDATE plugin_metadata SET version=3 WHERE id=1;
