// Package writer is the isolated Unicode PST writer v2 seam.
//
// This package is the isolated Unicode PST writer seam. It does not replace
// the legacy Create / BeginWrite APIs in the root module. PST-002 implements
// the 564-byte HEADER and 72-byte ROOT codecs. Finalize currently returns
// ErrNotImplemented until later NDB cards land.
//
// Target (locked by this package):
//   - Unicode PST wVer 23, new-file creation only
//   - Email folders, IPM.Note messages, recipients, by-value attachments
//   - Fail-closed unsupported features (ANSI, search folders, calendar,
//     contacts, tasks, OLE, in-place mutation, WIP crypt)
//
// Spec: Microsoft [MS-PST] v11.2.
package writer
