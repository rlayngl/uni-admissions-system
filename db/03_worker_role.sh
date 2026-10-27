#!/bin/sh
set -eu
psql -v ON_ERROR_STOP=1 -v worker_user="$WORKER_DB_USER" -v worker_password="$WORKER_DB_PASSWORD" -v db_name="$POSTGRES_DB" \
  --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<'SQL'
CREATE ROLE :"worker_user" LOGIN PASSWORD :'worker_password';
GRANT CONNECT ON DATABASE :"db_name" TO :"worker_user";
GRANT USAGE ON SCHEMA public TO :"worker_user";
GRANT SELECT ON users, applications, outbox_events TO :"worker_user";
GRANT SELECT, INSERT, UPDATE ON notifications TO :"worker_user";
GRANT UPDATE ON outbox_events TO :"worker_user";
GRANT USAGE, SELECT ON SEQUENCE notifications_notification_id_seq TO :"worker_user";
-- Worker cannot create/drop tables and cannot modify users, applications or documents.
SQL
