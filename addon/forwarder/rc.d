#!/bin/sh
#
#   rc.d script of lite-addon-forwarder (MIT), following the ccu-addon-kit service convention v1.
#   Linked as /usr/local/etc/config/rc.d/forwarder. openccu-lite runs it inside the unit
#   addon-forwarder.service as the addon's own user; start backgrounds the daemon and returns,
#   stop ends it cleanly, status prints one JSON line.
#

ADDON=forwarder
NAME="Forwarder"
CONF_DIR=/usr/local/etc/config
ADDON_DIR=/usr/local/addons/$ADDON
# in the addon's tree: a confined addon cannot write /var/run
PIDFILE=$ADDON_DIR/var/$ADDON.pid
DAEMON=$ADDON_DIR/bin/$ADDON
DAEMON_ARGS="-addon-dir $ADDON_DIR"

VERSION_ADDON=unknown
[ -r $ADDON_DIR/versions ] && . $ADDON_DIR/versions

log() { logger -t $ADDON -p daemon.$1 "$2"; }

json_str() { printf '%s' "$1" | tr -d '\000-\037' | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }

# inside openccu-lite's addon-$ADDON.service unit?
InUnit() { grep -q "addon-$ADDON.service" /proc/self/cgroup 2>/dev/null; }

# pid of the running daemon, empty when not running
Pid() {
    local pid
    [ -f $PIDFILE ] || return 1
    pid=`cat $PIDFILE 2>/dev/null`
    case "$pid" in ''|*[!0-9]*) return 1 ;; esac
    [ -d /proc/$pid ] || return 1
    case "`tr '\0' ' ' < /proc/$pid/cmdline 2>/dev/null`" in
        *$DAEMON*) echo $pid; return 0 ;;
    esac
    return 1
}

StartEpoch() {
    local ticks btime
    ticks=`sed -e 's/^.*) //' /proc/$1/stat 2>/dev/null | awk '{print $20}'`
    btime=`awk '/^btime/ {print $2}' /proc/stat`
    [ -n "$ticks" ] && [ -n "$btime" ] && echo $((btime + ticks / 100))
}

RssKb() { awk '/^VmRSS:/ {print $2}' /proc/$1/status 2>/dev/null; }

Status() {
    local pid rss since state code
    pid=`Pid`
    if [ -z "$pid" ]; then
        state=stopped; code=3
        if [ -f $PIDFILE ]; then state=failed; code=1; fi
        printf '{"schema":1,"running":false,"state":"%s","version":"%s"}\n' $state "`json_str "$VERSION_ADDON"`"
        return $code
    fi
    rss=`RssKb $pid`
    since=`StartEpoch $pid`
    printf '{"schema":1,"running":true,"state":"running","pid":%s,"rss_kb":%s,"since":%s,"version":"%s"}\n' \
        $pid ${rss:-0} ${since:-null} "`json_str "$VERSION_ADDON"`"
    return 0
}

Start() {
    if [ -n "`Pid`" ]; then echo "$ADDON already running"; return 0; fi
    [ -x $DAEMON ] || { echo "Starting $ADDON: FAILED ($DAEMON missing)"; log err "$DAEMON missing"; return 1; }
    rm -f $PIDFILE
    mkdir -p $ADDON_DIR/var $ADDON_DIR/etc
    # openccu-lite closes the addon's directory to other users (0751) before every start, and its
    # own server, which serves www/ (the 503 page), cannot open a tree it may not read: the
    # directory is the addon's own, so the addon opens it for reading again.
    if InUnit; then chmod 755 $ADDON_DIR 2>/dev/null; fi
    cd /
    log info "Starting $ADDON $VERSION_ADDON"
    $DAEMON $DAEMON_ARGS &
    echo $! > $PIDFILE
    sleep 1
    if [ -z "`Pid`" ]; then echo "Starting $ADDON: FAILED"; log err "start failed"; return 1; fi
    echo "Starting $ADDON: OK"
}

Stop() {
    local pid i
    pid=`Pid`
    if [ -z "$pid" ]; then rm -f $PIDFILE; echo "$ADDON not running"; return 0; fi
    log info "Stopping $ADDON (pid $pid)"
    kill $pid 2>/dev/null
    i=0
    while [ $i -lt 60 ] && [ -d /proc/$pid ]; do sleep 0.5; i=$((i + 1)); done
    if [ -d /proc/$pid ]; then log warn "Killing $ADDON (pid $pid)"; kill -9 $pid 2>/dev/null; sleep 1; fi
    [ -d /proc/$pid ] && { echo "Stopping $ADDON: FAILED"; return 1; }
    rm -f $PIDFILE
    echo "Stopping $ADDON: OK"
}

case "$1" in
    init) ;;
    start) Start; exit $? ;;
    stop) Stop; exit $? ;;
    restart) Stop; sleep 1; Start; exit $? ;;
    status) Status; exit $? ;;
    info)
        echo "Info: <div>$NAME</div>"
        echo "Name: $NAME"
        echo "Version: $VERSION_ADDON"
        echo "Config-Url: /addons/$ADDON/"
        echo "Operations: restart uninstall"
        ;;
    uninstall)
        Stop
        # openccu-lite runs this as the addon's user and removes the links, the proxy rule and the
        # menu entry itself afterwards. The program goes; etc/ (the targets, selections and
        # secrets) and var/ stay for a reinstall - remove /usr/local/addons/forwarder by hand to
        # be rid of them.
        rm -rf $ADDON_DIR/bin $ADDON_DIR/app $ADDON_DIR/www $ADDON_DIR/rc.d $ADDON_DIR/versions $ADDON_DIR/etc/lighttpd.conf
        rm -f $PIDFILE
        log info "Uninstalled $ADDON (settings kept in $ADDON_DIR)"
        ;;
    *) exit 0 ;;
esac
exit 0
