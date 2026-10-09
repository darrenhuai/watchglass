# The add-on's image: the published watchglass image for the add-on's
# version, started as root so run.sh can give the config folder the
# Supervisor creates (owned by root) to the watchglass user before running
# watchglass as that user. The Supervisor builds this on install and on
# every update, passing BUILD_VERSION (the version in config.yaml); there
# is nothing to compile, it only adds run.sh. The default is only for a
# build by hand.
ARG BUILD_VERSION=latest
FROM ghcr.io/darrenhuai/watchglass:${BUILD_VERSION}
USER root
COPY run.sh /usr/local/bin/watchglass-addon
ENTRYPOINT ["/bin/sh", "/usr/local/bin/watchglass-addon"]
# The same arguments as the plain image (an ENTRYPOINT resets the CMD it
# inherits).
CMD ["-config", "/config/config.yaml", "-db", "/config/watchglass.db", "-listen", "0.0.0.0:8080"]
