#!/bin/sh
# Exit immediately if a command exits with a non-zero status.
set -e

# Function to check if PostgreSQL is ready
wait_for_db() {
  echo "Waiting for database at db:5432..."
  # Use nc (netcat) which is usually available in alpine
  # Alternatively, install postgresql-client for pg_isready
  apk add --no-cache postgresql-client # Add this if using pg_isready
  timeout=60 # seconds
  start_time=$(date +%s)
  while ! pg_isready -h db -p 5432 -q -U "$POSTGRES_USER"; do
  # Or using netcat: while ! nc -z db 5432; do
    current_time=$(date +%s)
    elapsed_time=$((current_time - start_time))
    if [ $elapsed_time -ge $timeout ]; then
      echo "Error: Timed out waiting for database."
      exit 1
    fi
    echo -n "."
    sleep 2
  done
  echo "Database is ready!"
}

# Wait for the database to be ready
wait_for_db

# Run migrations (safe to run multiple times)
echo "Running database migrations..."
npx prisma migrate deploy

# Run seed script (ensure your seed script is idempotent!)
# Idempotent means running it multiple times doesn't cause errors or duplicate data.
# Usually achieved using `upsert` or checking existence before creating.
echo "Running database seed script..."
npx prisma db seed

echo "Initialization complete. Starting application..."

# Execute the original CMD from the Dockerfile
exec "$@"