// Package writer is the isolated Unicode PST writer v2 seam.
//
// This package is the isolated Unicode PST writer seam. It does not replace
// the legacy Create / BeginWrite APIs in the root module. PST-002 implements
// the 564-byte HEADER and 72-byte ROOT codecs. PST-003 implements AMap
// coverage from 0x4400, periodic PMap/FMap/FPMap/DList pages, and
// allocate/free/reserve on 64-byte slots. PST-004 implements Unicode page
// and external/internal block codecs (trailer, CRC, signature, BID, crypt
// boundaries). PST-005 implements Unicode NBT/BBT construction: first-key
// BTENTRY separators, page BIDs from bidNextP, and BBT reference-count
// ownership, Store-backed page allocation, persist via ROOT BREFs and
// bidNextB, and reopen that retains Store maps and allocated payloads.
// Extra BBT refs reopen as opaque unless PST-006 reconstructs them from
// XBLOCK/XXBLOCK rgbid and SLENTRY/SIENTRY. PST-006 streams data trees
// (direct/XBLOCK/XXBLOCK) into Store backing as blocks arrive and builds
// SLBLOCK plus one SIBLOCK (cLevel 0x01) over SLBLOCKs.
// Finalize currently returns ErrNotImplemented until later
// NDB cards land.
//
// Target (locked by this package):
//   - Unicode PST wVer 23, new-file creation only
//   - Email folders, IPM.Note messages, recipients, by-value attachments
//   - Fail-closed unsupported features (ANSI, search folders, calendar,
//     contacts, tasks, OLE, in-place mutation, WIP crypt)
//
// Spec: Microsoft [MS-PST] v11.2.
package writer
