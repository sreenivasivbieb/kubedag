# Runner image: the sandbox every step's command executes inside.
#
# busybox:1.36 is NOT Alpine and ships no package manager (no apk), so tini is
# copied from a throwaway Alpine stage rather than installed. Switching the base
# to Alpine would work but roughly doubles the image for no benefit here.
#
# IMPORTANT: this must be `tini-static`, not `tini`. Alpine's plain `tini`
# package is dynamically linked against musl, and busybox ships no musl loader,
# so the copy lands at /sbin/tini and then fails to execute with exit 127 --
# a failure that looks like "file not found" and is easy to miss. The static
# build has no interpreter dependency at all.
FROM alpine:3.20 AS tini-stage
RUN apk add --no-cache tini-static

FROM busybox:1.36

# tini reaps orphaned processes. Without it, a step doing `some-daemon &` leaves
# a zombie holding the container's PID table, and inter-lease cleanup cannot
# reliably enumerate what to kill. `cmd &` in user code is extremely common,
# so this is a baseline requirement, not a nicety.
COPY --from=tini-stage /sbin/tini-static /sbin/tini

# busybox caps GIDs at 60000, so the conventional 65532 "nonroot" uid cannot be
# created here. Use the uid/gid busybox already ships (nobody, 65534) rather than
# creating a user, and keep it consistent with runner-pod.yaml's runAsUser.
# Every place that needs the runner uid reads RUNNER_UID from one place.
RUN mkdir -p /work && chmod 1777 /work

USER 65534:65534
WORKDIR /work

ENTRYPOINT ["/sbin/tini", "--"]
CMD ["sleep", "infinity"]
