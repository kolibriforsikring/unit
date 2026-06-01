#!/usr/bin/env bash
set -e

# Why a script?
# unit intentionally avoids built-in secret manager integrations.
# This script is the interface: it can call any CLI or API you already use,
# without requiring unit to know or care where secrets come from.
#
# unit passes the environment name (e.g. 'dev', 'prod') as the first argument.

ENV=$1

echo "🤫 Sourcing secrets for environment: $ENV" >&2

# These are just local shell variables. 
# In a real app, you might use 'op read', 'lpass show', 'aws secretsmanager', etc.
if [ "$ENV" == "prod" ]; then
    API_KEY="prod-secret-api-key"
    DB_PASSWORD="prod-db-password"
else
    API_KEY="dev-secret-api-key"
    DB_PASSWORD="dev-db-password"
fi

# We export them to make them available to the 'env' command that 'unit' runs.
export DB_PASSWORD
export API_KEY
