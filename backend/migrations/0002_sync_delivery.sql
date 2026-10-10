CREATE TABLE sync_policies(
 project_id UUID NOT NULL,
 connection_id TEXT NOT NULL,
 record_id UUID NOT NULL REFERENCES records(id),
 state TEXT NOT NULL DEFAULT 'active' CHECK(state IN('active','ignored','archived_deleted')),
 revision BIGINT NOT NULL DEFAULT 1,
 historical_path TEXT NOT NULL DEFAULT '',
 historical_created TEXT NOT NULL DEFAULT '',
 reason TEXT NOT NULL DEFAULT '',
 checked_at TIMESTAMPTZ,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(connection_id,record_id)
);
CREATE TABLE sync_deliveries(
 device_id UUID NOT NULL REFERENCES devices(id),
 record_id UUID NOT NULL REFERENCES records(id),
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN('pending','retry_wait','needs_action','confirmed')),
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 error_code TEXT NOT NULL DEFAULT '',
 media_verified BOOLEAN NOT NULL DEFAULT FALSE,
 record_written BOOLEAN NOT NULL DEFAULT FALSE,
 status_verified BOOLEAN NOT NULL DEFAULT FALSE,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(device_id,record_id)
);
CREATE TABLE sync_delivery_events(
 cursor BIGSERIAL PRIMARY KEY,
 project_id UUID NOT NULL,
 connection_id TEXT NOT NULL,
 record_id UUID NOT NULL REFERENCES records(id),
 device_id UUID,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX sync_delivery_events_scope ON sync_delivery_events(connection_id,cursor);
CREATE TABLE sync_delivery_ops(
 connection_id TEXT NOT NULL,
 op_id TEXT NOT NULL,
 request_hash TEXT NOT NULL,
 actor TEXT NOT NULL,
 record_id UUID NOT NULL REFERENCES records(id),
 result JSONB NOT NULL,
 request JSONB NOT NULL,
 state TEXT NOT NULL DEFAULT 'queued',
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(connection_id,op_id)
);
UPDATE plugin_metadata SET version=2 WHERE id=1;
