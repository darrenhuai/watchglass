#!/bin/sh
# Entry point of the add-on's image (addon/Dockerfile).
#
# The Supervisor mounts the add-on's config folder at /config, owned by
# root, and watchglass runs as the unprivileged watchglass user, as in the
# plain image. That user can't create config.yaml in a root-owned folder,
# so the add-on's image starts as root, hands the folder and what's in it
# to the watchglass user (files the File editor or Samba add-on wrote as
# root included: the dashboard rewrites config.yaml and the history
# database), and then runs watchglass as that user.
set -eu
if [ "$(id -u)" = 0 ]; then
	chown -hR watchglass:watchglass /config
	exec setpriv --reuid=watchglass --regid=watchglass --init-groups --no-new-privs -- /usr/local/bin/watchglass "$@"
fi
exec /usr/local/bin/watchglass "$@"
