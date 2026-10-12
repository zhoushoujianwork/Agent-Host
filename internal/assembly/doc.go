// Package assembly validates a project's working directory and shared runtime
// configuration before native execution. Host memory remains scoped separately
// from the working directory; harnesses own their native instructions and loops.
package assembly
