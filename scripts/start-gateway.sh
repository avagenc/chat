#!/bin/sh
set -e

# Validation
if [ -z "$SUPABASE_PUBLIC_KEY" ]; then
    echo "Error: SUPABASE_PUBLIC_KEY is not set"
    exit 1
fi

echo "Decoding SUPABASE_PUBLIC_KEY..."
# Decode base64. If it fails, script exits due to set -e (needs pipefail or manual check)
DECODED_KEY=$(echo "$SUPABASE_PUBLIC_KEY" | base64 -d)

if [ -z "$DECODED_KEY" ]; then
    echo "Error: DECODED_KEY is empty"
    exit 1
fi

# Indent subsequent lines by 10 spaces to fit YAML structure
# The first line is kept as-is because the template has it properly positioned.
FORMATTED_KEY=$(echo "$DECODED_KEY" | sed 's/^/          /' | sed '1s/^          //')

export SUPABASE_PUBLIC_KEY="$FORMATTED_KEY"

echo "Generating configuration..."
envsubst < /usr/local/kong/declarative/kong.yaml.template > /usr/local/kong/declarative/kong.yaml
envsubst < /usr/local/kong/policies/enforcer.lua.template > /usr/local/kong/policies/enforcer.lua

echo "Starting Kong..."
exec /docker-entrypoint.sh kong docker-start
