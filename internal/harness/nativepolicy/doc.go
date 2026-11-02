// Package nativepolicy assembles Host policy and decodes native observations
// across the Offload public Go package boundary. Offload owns native execution,
// authentication, timeouts and cleanup; Host owns policy and completion gates.
package nativepolicy
