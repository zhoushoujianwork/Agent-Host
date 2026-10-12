package state

const schemaRoutingV9 = `
ALTER TABLE projects ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN aliases BLOB NOT NULL DEFAULT '[]';
ALTER TABLE channel_messages ADD COLUMN route_order INTEGER NOT NULL DEFAULT 0;
CREATE TABLE channel_selections(binding TEXT PRIMARY KEY, payload BLOB NOT NULL);
INSERT INTO channel_selections(binding,payload) SELECT binding,json_object('mode','manual','project_ids',CASE WHEN project_id='' THEN json('[]') ELSE json_array(project_id) END,'primary_id',project_id) FROM channel_projects;
CREATE TABLE channel_choices(id TEXT PRIMARY KEY, channel_message_id TEXT NOT NULL UNIQUE REFERENCES channel_messages(id), binding TEXT NOT NULL, status TEXT NOT NULL, confirmation_message_id TEXT NOT NULL DEFAULT '', payload BLOB NOT NULL);
CREATE INDEX channel_choices_pending ON channel_choices(binding,status);
PRAGMA user_version=9;
`
