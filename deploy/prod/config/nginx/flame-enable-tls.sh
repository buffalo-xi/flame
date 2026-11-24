#!/bin/sh
# Decides whether the TLS server block is part of this container's configuration.
#
# Installed as /docker-entrypoint.d/40-flame-tls.sh, which the nginx image runs before it validates and starts
# nginx. It has to happen at container start rather than at install time: the certificate is a host file
# mounted into the container, and the image is immutable.
#
# Both branches are idempotent, so a `docker compose up -d` after dropping a certificate in, or after removing
# one, does the right thing without editing any file.
set -eu

conf=/etc/nginx/conf.d/20-flame-tls.conf

if [ -s /etc/nginx/tls/fullchain.pem ] && [ -s /etc/nginx/tls/privkey.pem ]; then
    ln -sf /etc/nginx/flame/flame-tls.conf "$conf"
    echo "flame: certificate found, TLS server block enabled on 27443"
else
    rm -f "$conf"
    echo "flame: no certificate in /etc/nginx/tls, 27443 stays closed"
fi
