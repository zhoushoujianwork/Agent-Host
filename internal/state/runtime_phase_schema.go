package state

const SchemaVersion = 15

const schemaChannelReactionV14 = `
ALTER TABLE channel_reactions ADD COLUMN provider_reaction_id TEXT NOT NULL DEFAULT '';
PRAGMA user_version=14;
`

// Occurrence zero preserves singleton stages. Repeatable phase transitions use
// their durable event sequence, keeping history and delivery attempts distinct.
const schemaRuntimePhasesV8 = `
CREATE TABLE channel_reactions_v8(id INTEGER PRIMARY KEY AUTOINCREMENT, channel_message_id TEXT NOT NULL REFERENCES channel_messages(id), profile TEXT NOT NULL, stage TEXT NOT NULL CHECK(length(stage) BETWEEN 1 AND 64), label TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('ready','sending','accepted','failed','unknown','skipped')), error TEXT NOT NULL DEFAULT '', active INTEGER NOT NULL DEFAULT 0 CHECK(active IN (0,1)), removal_status TEXT NOT NULL DEFAULT 'pending' CHECK(removal_status IN ('pending','sending','accepted','failed','unknown')), removal_error TEXT NOT NULL DEFAULT '', source_run_id TEXT NOT NULL DEFAULT '', source_event_seq INTEGER NOT NULL DEFAULT 0 CHECK(source_event_seq>=0), occurrence INTEGER NOT NULL DEFAULT 0 CHECK(occurrence>=0), created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(channel_message_id,stage,occurrence), CHECK((source_run_id='' AND source_event_seq=0 AND occurrence=0) OR (source_run_id<>'' AND source_event_seq>0 AND occurrence IN (0,source_event_seq))));
INSERT INTO channel_reactions_v8(id,channel_message_id,profile,stage,label,status,error,active,removal_status,removal_error,source_run_id,source_event_seq,occurrence,created_at,updated_at) SELECT id,channel_message_id,profile,stage,label,status,error,active,removal_status,removal_error,source_run_id,source_event_seq,CASE WHEN stage='execute' AND source_run_id<>'' THEN source_event_seq ELSE 0 END,created_at,updated_at FROM channel_reactions;
UPDATE sqlite_sequence SET seq=MAX(seq,COALESCE((SELECT seq FROM sqlite_sequence WHERE name='channel_reactions'),0)) WHERE name='channel_reactions_v8';
DROP TABLE channel_reactions;
ALTER TABLE channel_reactions_v8 RENAME TO channel_reactions;
CREATE INDEX channel_reactions_queue ON channel_reactions(profile,status,id);
CREATE UNIQUE INDEX channel_reactions_terminal ON channel_reactions(channel_message_id) WHERE stage IN ('completed','failed');
PRAGMA user_version=8;
`
