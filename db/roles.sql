-- PostgreSQL init script; executed by the official image after schema.sql.
-- Environment variables are injected by docker-compose, not committed to Git.
\getenv app_user APP_DB_USER
\getenv app_password APP_DB_PASSWORD
\getenv db_name POSTGRES_DB

CREATE ROLE :"app_user" LOGIN PASSWORD :'app_password';
GRANT CONNECT ON DATABASE :"db_name" TO :"app_user";
GRANT USAGE ON SCHEMA public TO :"app_user";
GRANT SELECT ON users, user_roles, roles, document_types, applications,
  application_status_history, documents, document_issue_events, audit_events,
  notifications, outbox_events, ai_checks, password_reset_tokens TO :"app_user";
GRANT INSERT ON users, user_roles, applications, application_status_history,
  documents, document_issue_events, audit_events, outbox_events, password_reset_tokens TO :"app_user";
GRANT UPDATE ON users, applications, password_reset_tokens TO :"app_user";
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO :"app_user";
-- No CREATE, DROP or DELETE privilege is granted to the API role.
