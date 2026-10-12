# Runner image: the sandbox in which step commands execute.
#
# Deliberately minimal. A larger base (ubuntu, python, etc.) would make cold
# start slower and attack surface larger. busybox gives us a shell and a
# handful of coreutils, which is enough for the reference workflow.
#
# NOTE: busybox:1.36 is NOT Alpine and ships no package manager (no apk), so
# tini is copied in from a throwaway Alpine stage rather than installed. This
# keeps the final image at busybox size instead of switching to Alpine.
FROM alpine:3.20 AS tini-stage
RUN apk add --no-cache tini

FROM busybox:1.36

# tini reaps orphaned processes. Without it, a step that forks a background
# process leaves a zombie holding the container's PID table, and our cleanup
# between leases cannot reliably enumerate what to kill. This is not a
# theoretical concern: `some-command &` in user code is extremely common.
COPY --from=tini-stage /sbin/tini /sbin/tini

# Non-root user with a fixed uid so that Phase 9's runAsNonRoot and the
# StatefulSet's runAsUser agree. busybox provides addgroup/adduser.
RUN addgroup -g 65532 kubedag && \
    adduser -D -u 65532 -G kubedag kubedag

RUN mkdir -p /work && chown kubedag:kubedag /work && chmod 0777 /work

USER 65532:65532
WORKDIR /work

ENTRYPOINT ["/sbin/tini", "--"]
CMD ["sleep", "infinity"]