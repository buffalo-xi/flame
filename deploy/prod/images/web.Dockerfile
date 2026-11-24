# syntax=docker/dockerfile:1
#
# flame/web: nginx plus the built desktop bundle. The only container that publishes a port facing the outside.
#
# Certificates and the shared storage are *not* in the image: they are host files mounted in, so renewing a
# certificate or restoring a backup never needs a rebuild.
#
# Build: docker build -f images/web.Dockerfile -t flame/web:<tag> .

# `stable-alpine` rather than a pinned minor: nginx is the one container that should track its security
# updates, and nothing in these configs is version-sensitive.
FROM nginx:stable-alpine

# The package's default site listens on :80 with a different root; this image has two entry points of its own.
RUN rm -f /etc/nginx/conf.d/default.conf

# http-context definitions first (a `map` may only be defined once), then the plain server block, then the
# TLS block outside conf.d - the entrypoint hook links it in only when a certificate is present.
COPY config/nginx/flame-common.conf    /etc/nginx/conf.d/00-flame-common.conf
COPY config/nginx/flame-http.conf      /etc/nginx/conf.d/10-flame-http.conf
COPY config/nginx/flame-locations.inc  /etc/nginx/flame/locations.inc
COPY config/nginx/flame-tls.conf       /etc/nginx/flame/flame-tls.conf

# Runs from the nginx image's own entrypoint before nginx is validated and started.
COPY config/nginx/flame-enable-tls.sh  /docker-entrypoint.d/40-flame-tls.sh
RUN chmod 0755 /docker-entrypoint.d/40-flame-tls.sh

COPY out/web/ /usr/share/nginx/html/

EXPOSE 27001
EXPOSE 27443
