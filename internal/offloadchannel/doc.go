// Package offloadchannel shares direct Offload execution between authenticated
// channel conversations and local-owner one-shot Chat. Host implements
// channel.Host; optional AGH memory reuses existing retrieval and publication.
//
// Offload owns its model and tool loop. This package records Host-side evidence:
// source provenance, actual native session and canonical profile, and separate
// execution and memory outcomes. A successful run is execution evidence only,
// not business acceptance. Native work is never retried, replayed or sent to
// another engine implicitly.
package offloadchannel
