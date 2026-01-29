FROM kong:3.4

USER root

COPY config/kong.yaml /usr/local/kong/declarative/kong.yaml
COPY policies/enforcer.lua /usr/local/kong/policies/enforcer.lua

ENV KONG_DATABASE="off" \
    KONG_DECLARATIVE_CONFIG="/usr/local/kong/declarative/kong.yaml" \
    KONG_LUA_PACKAGE_PATH="/usr/local/kong/policies/?.lua;;" \
    KONG_UNTRUSTED_LUA="on" \
    KONG_LUA_SSL_TRUSTED_CERTIFICATE="/etc/ssl/certs/ca-certificates.crt" \
    KONG_LUA_SSL_VERIFY_DEPTH="2" \
    KONG_PROXY_ACCESS_LOG="/dev/stdout" \
    KONG_ADMIN_ACCESS_LOG="/dev/stdout" \
    KONG_PROXY_ERROR_LOG="/dev/stderr" \
    KONG_ADMIN_ERROR_LOG="/dev/stderr"

USER kong
