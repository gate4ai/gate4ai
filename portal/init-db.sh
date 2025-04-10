#!/bin/sh
# /mcp/portal/init-db.sh

# Exit immediately if a command exits with a non-zero status.
set -e

# Ensure required environment variables are set
: "${POSTGRES_USER?Error: POSTGRES_USER environment variable is required.}"
: "${GATE4AI_DATABASE_URL?Error: GATE4AI_DATABASE_URL environment variable is required.}"

echo "[init-db.sh] Starting database initialization..."

# Install psql client needed for pg_isready
echo "[init-db.sh] Installing postgresql-client..."
apk add --no-cache postgresql-client

# Wait for the database to be ready
echo "[init-db.sh] Waiting for database at db:5432 (User: $POSTGRES_USER)..."
retries=60
db_ready=0
while [ "$retries" -gt 0 ]; do
  # Use pg_isready directly, variables are from the container's env
  if pg_isready -h db -p 5432 -q -U "$POSTGRES_USER"; then
    db_ready=1
    break
  fi
  retries=$((retries - 1))
  echo -n "."
  sleep 2
done

if [ "$db_ready" -eq 0 ]; then
  echo "[init-db.sh] Error: Timed out waiting for database after 120 seconds."
  exit 1
fi

echo "[init-db.sh] Database is ready."

# Run Prisma migrations
echo "[init-db.sh] Running Prisma migrations..."
# Note: GATE4AI_DATABASE_URL is used by Prisma commands automatically
npx prisma migrate deploy

# Run Prisma seed script
echo "[init-db.sh] Running Prisma seed..."
npx prisma db seed

echo "[init-db.sh] Initialization complete."
exit 0