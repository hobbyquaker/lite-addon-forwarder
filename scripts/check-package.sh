#!/bin/sh
#
#   Checks that an addon package holds everything the system needs to install and run it: the
#   installer, the manifest, the daemon, the rc.d script, the proxy rule (etc/lighttpd.conf - a
#   package without it serves no page), the page, the icons, the 503 page, the version file; and
#   that no configuration or secret came along.
#
#     scripts/check-package.sh dist/forwarder-*-0.2.0.tar.gz
#
set -e
[ $# -gt 0 ] || { echo "usage: $0 <package.tar.gz>..." >&2; exit 2; }
status=0
for pkg in "$@"; do
    list=$(tar tzf "$pkg" | sed 's#^\./##')
    for want in update_script openccu-lite.json forwarder/bin/forwarder forwarder/rc.d forwarder/etc/lighttpd.conf \
        forwarder/app/index.html forwarder/www/icon.svg forwarder/www/icon-dark.svg forwarder/www/lighttpd-error-503.html forwarder/versions; do
        if ! printf '%s\n' "$list" | grep -qx "$want"; then
            echo "$pkg: missing $want" >&2
            status=1
        fi
    done
    for bad in forwarder/etc/forwarder.json forwarder/etc/secrets.json; do
        if printf '%s\n' "$list" | grep -qx "$bad"; then
            echo "$pkg: must not contain $bad" >&2
            status=1
        fi
    done
    if printf '%s\n' "$list" | grep -q '^forwarder/var/'; then
        echo "$pkg: must not contain forwarder/var/" >&2
        status=1
    fi
    [ $status -eq 0 ] && echo "$pkg: ok"
done
exit $status
