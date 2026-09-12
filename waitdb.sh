#!/usr/bin/env bash
# Bound disposable database readiness and create its database once.
set -euo pipefail

output=
fail() {
    printf 'ODBC startup: %s\n' "$1" >&2
    if [[ -n "$output" ]]; then printf '%s\n' "$output" >&2; fi
    exit 1
}

backend=${1:-}
container=${2:-}
[[ -n "$container" ]] || fail 'missing container name'
case "$backend" in
    mssql) pattern='SQL.Server.is.now.ready.for.client.connections'; name_limit=128 ;;
    mysql) pattern='^Version.*port:.3306'; name_limit=64 ;;
    *) fail 'backend must be mssql or mysql' ;;
esac
[[ ${DB_NAME:-} =~ ^[A-Za-z_][A-Za-z0-9_]*$ && ${#DB_NAME} -le $name_limit ]] || fail 'DB_NAME must be a plain SQL identifier within the backend length limit'

ready_timeout=${ODBC_READY_TIMEOUT:-120}
command_timeout=${ODBC_COMMAND_TIMEOUT:-10}
for value in "$ready_timeout" "$command_timeout"; do
    [[ "$value" =~ ^[1-9][0-9]{0,3}$ && "$value" -le 3600 ]] || fail 'timeouts must be integer seconds from 1 to 3600'
done
if command -v timeout >/dev/null 2>&1; then
    timeout_cmd=timeout
elif command -v gtimeout >/dev/null 2>&1; then
    timeout_cmd=gtimeout
else
    fail 'GNU timeout or gtimeout is required'
fi

deadline=$((SECONDS + ready_timeout))
bounded() {
    local remaining=$((deadline - SECONDS))
    ((remaining > 0)) || return 124
    local limit=$command_timeout
    if ((remaining < limit)); then limit=$remaining; fi
    "$timeout_cmd" --kill-after=1s "${limit}s" "$@"
}

# Keep only the last 4 KiB; failed commands cannot produce unlimited diagnostics.
capture() {
    output=$(bounded "$@" 2>&1 | tail -c 4096)
}

while :; do
    capture docker inspect --format '{{.State.Running}}' "$container" || fail 'container inspection failed or timed out'
    [[ "$output" == true ]] || fail 'container stopped before readiness'
    capture docker logs --tail 200 "$container" || fail 'readiness logs failed or timed out'
    if grep -Eq "$pattern" <<< "$output"; then break; fi
    bounded sleep 1 || fail 'readiness deadline exceeded'
done

# Existing databases are success. Creation errors are permanent for this attempt.
# Transfer the query through an environment variable, never through shell source.
if [[ "$backend" == mssql ]]; then
    printf -v ODBC_TEST_DB_QUERY "IF DB_ID(N'%s') IS NULL CREATE DATABASE [%s]" "$DB_NAME" "$DB_NAME"
    export ODBC_TEST_DB_QUERY
    capture docker exec -e ODBC_TEST_DB_QUERY "$container" sh -c \
        'SQLCMDPASSWORD="$MSSQL_SA_PASSWORD" /opt/mssql-tools18/bin/sqlcmd -b -S localhost -U SA -Q "$ODBC_TEST_DB_QUERY"' \
        || fail 'database creation failed or timed out'
else
    printf -v ODBC_TEST_DB_QUERY 'CREATE DATABASE IF NOT EXISTS `%s`' "$DB_NAME"
    export ODBC_TEST_DB_QUERY
    capture docker exec -e ODBC_TEST_DB_QUERY "$container" sh -c \
        'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -hlocalhost -uroot -e "$ODBC_TEST_DB_QUERY"' \
        || fail 'database creation failed or timed out'
fi
printf 'ODBC startup: database %s is ready\n' "$DB_NAME"
