// Package audit records how each decision was reached, for the audit trail.
//
// An audit record is not a log line. Logs and metrics must carry no personal
// data (INV8), so they name a field without its value; an audit record may
// carry identifiers because the audit store is access-controlled, and it has
// to, because "why was this employee refused" is a question a support engineer
// and a regulator both ask months later.
//
// Writing a record must never slow or fail a decision. The async logger
// therefore drops a record rather than blocking, and counts every drop so that
// a lost record is visible instead of silent.
package audit
