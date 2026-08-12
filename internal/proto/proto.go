// Package proto pins kibitz's wire-protocol domain label. Every parley entry
// point that derives keys or session IDs must receive this label — it is what
// keeps new builds byte-compatible with deployed clients and the hosted
// relay. Changing it is a protocol version bump: clients on different labels
// derive different session IDs and keys and cannot talk to each other.
package proto

// Label is passed to session.WithProtocol and phrase.SessionID everywhere.
const Label = "kibitz/v1"
