// Package offloadchannel runs channel conversations directly on the Offload
// CLI. Host implements channel.Host without the native Host, harness.Runner,
// Jev decision client or learned-memory closeout.
//
// Offload owns its model and tool loop. This package records the Host-side
// evidence: the channel-linked run, the actual Offload session and canonical
// profile, and the execution outcome. A successful run is execution evidence
// only; it is not business acceptance. A failed or canceled run is never
// retried, replayed or sent to another engine.
package offloadchannel
