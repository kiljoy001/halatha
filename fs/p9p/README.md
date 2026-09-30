The browser 9P2000 server returns whole stat records for directory reads,
including when the requested count or negotiated msize limits the response.
A count too small for the next record returns an error. Directory offsets
must be zero or the end of the previous successful read, as required by
[read(5)](https://9p.io/magic/man2html/5/read).

Independent fids execute concurrently. Operations using the same fid (or
both fids of a walk) execute in arrival order. Flush cancels the request
context and suppresses its response before acknowledging the flush; tag
reuse cannot revive an old response. Filesystem implementations that do not
honor context cancellation can continue running, and later operations on
that fid wait for them. Flush cannot undo a filesystem mutation already
performed. See [flush(5)](https://9p.io/magic/man2html/5/flush).

RAM files expose `FileIdentity() (uint64, uint32)` on their FileInfo. The ID
survives renames, replacements get fresh IDs, and successful writes and
truncates increment the version immediately. Handles share live contents;
Close does not commit a private snapshot. Independent cloned files get new
identities. Metadata wrappers preserve the source identity.

Backends without a native FileIdentity still use pathname-derived QIDs with
version zero: they cannot promise cacheable versions or identity continuity
across external renames/replacements. Such backends must implement the
identity contract before clients can rely on those guarantees. Synthetic
mount-table directories also use this fallback.

The direct 9legacy example exports its browser service namespace without
importing the broader server filesystem into it. Its previous hostfs boot-log
mirror is removed. Boot logs remain available in the browser namespace and
existing health telemetry remains active.

The example has no automatic file or VM-state persistence. Guest edits and
browser RAM files are temporary, including files queued under downloads/.
Work survives a reload only after the user completes a save to a local file
or retains a downloaded export. A successful 9P write or Close means the RAM
contents were updated; it is not a durable-storage acknowledgment.
