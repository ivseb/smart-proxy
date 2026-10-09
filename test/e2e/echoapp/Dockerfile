# Built by run.sh from a binary compiled on the host (no base image to pull).
FROM scratch
COPY echoapp /echoapp
USER 65534
ENTRYPOINT ["/echoapp"]
