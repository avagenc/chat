FROM kong:3.4

USER root

RUN apt-get update && apt-get install -y gettext-base && rm -rf /var/lib/apt/lists/*

COPY config/kong.yaml /usr/local/kong/declarative/kong.yaml.template
COPY policies/enforcer.lua.template /usr/local/kong/policies/enforcer.lua.template
COPY scripts/start-gateway.sh /usr/local/bin/start-gateway.sh

RUN chmod +x /usr/local/bin/start-gateway.sh

RUN chown -R kong:kong /usr/local/kong/declarative /usr/local/kong/policies

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

EXPOSE 8000

USER kong

CMD ["/usr/local/bin/start-gateway.sh"]
