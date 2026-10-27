#!/usr/bin/env bash
# Assertions against a running local Compose stack. Requires curl, jq, Docker Compose and .env.
set -euo pipefail
BASE="${BASE:-http://localhost:8080/api/v1}"
for command_name in curl jq docker; do
  command -v "$command_name" >/dev/null || { echo "$command_name is required" >&2; exit 1; }
done
[[ -f .env ]] || { echo 'Run from project root after copying .env.example to .env' >&2; exit 1; }
set -a; . ./.env; set +a

expect_status() {
  local expected="$1"; shift
  local tmp code
  tmp=$(mktemp)
  if ! code=$(curl -sS -o "$tmp" -w '%{http_code}' "$@"); then
    cat "$tmp" >&2 || true; rm -f "$tmp"; echo 'curl transport failure' >&2; return 1
  fi
  if [[ "$code" != "$expected" ]]; then
    echo "Expected HTTP $expected, received HTTP $code for: $*" >&2
    cat "$tmp" >&2; rm -f "$tmp"; return 1
  fi
  cat "$tmp"
  rm -f "$tmp"
}

login() { expect_status 200 -X POST "$BASE/auth/login" -H 'Content-Type: application/json' -d "$(jq -nc --arg u "$1" --arg p "$2" '{university_id:$u,password:$p}')"; }
U1=$(login student01 'Student-Demo-2026!')
U2=$(login student02 'Student-Demo-2026!')
ADM=$(login staff01 'Staff-Demo-2026!')
T1=$(jq -r .access_token <<<"$U1")
T2=$(jq -r .access_token <<<"$U2")
TA=$(jq -r .access_token <<<"$ADM")
[[ -n "$T1" && "$T1" != null && -n "$T2" && "$T2" != null && -n "$TA" && "$TA" != null ]] || { echo 'Login response did not contain all tokens' >&2; exit 1; }

echo '=== 1. Cross-owner access: USER student01 reads student02 application; expected 404 ==='
expect_status 404 "$BASE/applications/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" -H "Authorization: Bearer $T1" >/dev/null
echo 'PASS: unauthorized object is masked with 404.'

echo '=== 2. Mass-assignment attempt: owner/status/role; expected 400 and no insert ==='
expect_status 400 -X POST "$BASE/applications" -H "Authorization: Bearer $T1" -H 'Content-Type: application/json' \
  -d '{"document_type_code":"STUDY_CERT","comment":"attack","owner_user_id":"22222222-2222-4222-8222-222222222222","status":"issued","role":"ADMIN"}' >/dev/null
echo 'PASS: strict JSON decoder rejected unknown fields.'

echo '=== 3. Valid creation; expected 201 ==='
IDEMPOTENCY_KEY="smoke-$(date +%s)-$$"
CREATE_BODY='{"document_type_code":"STUDY_CERT","comment":"created by security smoke test"}'
CREATED=$(expect_status 201 -X POST "$BASE/applications" -H "Authorization: Bearer $T1" -H 'Content-Type: application/json' -H "Idempotency-Key: $IDEMPOTENCY_KEY" -d "$CREATE_BODY")
printf '%s\n' "$CREATED" | jq .
APP_ID=$(jq -r .application_id <<<"$CREATED")
[[ -n "$APP_ID" && "$APP_ID" != null ]] || { echo 'Could not extract application ID' >&2; exit 1; }
REPLAY=$(expect_status 201 -X POST "$BASE/applications" -H "Authorization: Bearer $T1" -H 'Content-Type: application/json' -H "Idempotency-Key: $IDEMPOTENCY_KEY" -d "$CREATE_BODY")
REPLAY_ID=$(jq -r .application_id <<<"$REPLAY")
[[ "$REPLAY_ID" == "$APP_ID" ]] || { echo "Idempotency replay changed ID: $APP_ID -> $REPLAY_ID" >&2; exit 1; }
expect_status 409 -X POST "$BASE/applications" -H "Authorization: Bearer $T1" -H 'Content-Type: application/json' -H "Idempotency-Key: $IDEMPOTENCY_KEY" \
  -d '{"document_type_code":"STUDY_CERT","comment":"different body for same key"}' >/dev/null
echo 'PASS: same key and body replays the same app; the same key with a different body is 409.'

echo '=== 4. Check DB row: owner=student01 and status=submitted ==='
ROW=$(docker compose exec -T db psql -qAt -F '|' -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c \
  "SELECT u.university_id,a.status,a.service_unit_code FROM applications a JOIN users u ON u.user_id=a.owner_user_id WHERE a.application_id='$APP_ID'::uuid;")
printf 'DB: %s\n' "$ROW"
[[ "$ROW" == *'student01|submitted|STUDENT'* ]] || { echo "Unexpected owner/status row: $ROW" >&2; exit 1; }
COUNT=$(docker compose exec -T db psql -qAt -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT count(*) FROM applications WHERE owner_user_id='11111111-1111-4111-8111-111111111111'::uuid AND idempotency_key='$IDEMPOTENCY_KEY';")
[[ "$COUNT" == '1' ]] || { echo "Expected exactly one idempotent application; found $COUNT" >&2; exit 1; }
echo 'PASS: owner, status, unit and one-row idempotency are verified in PostgreSQL.'

echo '=== 5. List with contract filters and pagination; expected 200 ==='
expect_status 200 "$BASE/applications?status=submitted&document_type_code=STUDY_CERT&page=1&page_size=20" -H "Authorization: Bearer $T1" | jq . >/dev/null
echo 'PASS: list filters and pagination are accepted.'

echo '=== 6. AI endpoint is an authorized 501 stub by task exception ==='
expect_status 501 -X POST "$BASE/applications/$APP_ID/ai/completeness-check" -H "Authorization: Bearer $T1" -H 'Content-Type: application/json' -d '{}' >/dev/null
echo 'PASS: LLM endpoint is not implemented, as explicitly allowed by LR-3.'

echo '=== 7. USER calls privileged endpoint; expected 403 ==='
expect_status 403 -X PATCH "$BASE/applications/$APP_ID/status" -H "Authorization: Bearer $T1" -H 'Content-Type: application/json' -d '{"status":"approved","reason":"must be denied"}' >/dev/null
echo 'PASS: RBAC is enforced by API.'

echo '=== 8. ADMIN transitions submitted -> in_review -> approved ==='
expect_status 200 -X PATCH "$BASE/applications/$APP_ID/status" -H "Authorization: Bearer $TA" -H 'Content-Type: application/json' -d '{"status":"in_review","reason":"review started"}' | jq .
expect_status 200 -X PATCH "$BASE/applications/$APP_ID/status" -H "Authorization: Bearer $TA" -H 'Content-Type: application/json' -d '{"status":"approved","reason":"requirements met"}' | jq .

echo '=== 9. Issue once; expected 201, repeat expected 409 ==='
ISSUED=$(expect_status 201 -X POST "$BASE/applications/$APP_ID/issue" -H "Authorization: Bearer $TA" -H 'Content-Type: application/json' -d '{"document_title":"Справка с места учёбы"}')
printf '%s\n' "$ISSUED" | jq .
DOC_ID=$(jq -r .document_id <<<"$ISSUED")
[[ -n "$DOC_ID" && "$DOC_ID" != null ]] || { echo 'Could not extract document ID' >&2; exit 1; }
expect_status 409 -X POST "$BASE/applications/$APP_ID/issue" -H "Authorization: Bearer $TA" -H 'Content-Type: application/json' -d '{"document_title":"Справка с места учёбы"}' >/dev/null
echo 'PASS: repeated issue did not create a second document.'

echo '=== 10. Owner reads issued document; expected 200 ==='
expect_status 200 "$BASE/documents/$DOC_ID" -H "Authorization: Bearer $T1" | jq .

echo '=== 11. Another USER reads that document; expected 404 ==='
expect_status 404 "$BASE/documents/$DOC_ID" -H "Authorization: Bearer $T2" >/dev/null
echo 'PASS: document ownership is enforced.'

echo '=== 12. App-role DDL denial ==='
if docker compose exec -T -e PGPASSWORD="$APP_DB_PASSWORD" db psql -q -v ON_ERROR_STOP=1 -h 127.0.0.1 -U "$APP_DB_USER" -d "$POSTGRES_DB" -c 'CREATE TABLE should_be_denied(id integer);' >/dev/null 2>&1; then
  echo 'FAIL: application role unexpectedly created a table' >&2; exit 1
fi
if docker compose exec -T -e PGPASSWORD="$APP_DB_PASSWORD" db psql -q -v ON_ERROR_STOP=1 -h 127.0.0.1 -U "$APP_DB_USER" -d "$POSTGRES_DB" -c 'DROP TABLE applications;' >/dev/null 2>&1; then
  echo 'FAIL: application role unexpectedly dropped a table' >&2; exit 1
fi
echo 'PASS: CREATE TABLE and DROP TABLE are denied for application role.'

echo '=== 13. ADMIN audit list; expected 200 ==='
expect_status 200 "$BASE/audit/events" -H "Authorization: Bearer $TA" | jq . >/dev/null
echo 'PASS: privileged business audit endpoint is accessible to ADMIN.'

echo '=== 14. Logout revokes T2 session; expected 200 then 401 ==='
expect_status 200 -X POST "$BASE/auth/logout" -H "Authorization: Bearer $T2" >/dev/null
expect_status 401 "$BASE/applications/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" -H "Authorization: Bearer $T2" >/dev/null
echo 'PASS: the revoked opaque token is rejected.'

echo '=== 15. Broker/worker evidence ==='
docker compose exec -T rabbitmq rabbitmqctl list_queues name messages_ready messages_unacknowledged
echo 'Open Mailpit at http://localhost:8025 and check worker logs with: docker compose logs --tail=100 worker'
echo 'ALL SCRIPT ASSERTIONS PASSED against the running local system.'
