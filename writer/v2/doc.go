// Package writer is the isolated Unicode PST writer v2 seam.
//
// This package is the PST-001 contract and conformance oracle. It does not
// replace the legacy Create / BeginWrite APIs in the root module. Later PST-*
// cards implement codecs behind this API; Finalize currently returns
// ErrNotImplemented for on-disk PST bytes.
//
// Target (locked by this package):
//   - Unicode PST wVer 23, new-file creation only
//   - Email folders, IPM.Note messages, recipients, by-value attachments
//   - Fail-closed unsupported features (ANSI, search folders, calendar,
//     contacts, tasks, OLE, in-place mutation, WIP crypt)
//
// Spec: Microsoft [MS-PST] v11.2.
package writer
