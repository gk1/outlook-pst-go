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
// into a FileSink spool, commits through CommitTo without assembling
// FileEOF, and reopens via OpenNDBFrom from any io.ReaderAt (not only
// Sink). PST-007 is the transaction/crash-safety boundary: one snapshot
// covers allocator, catalog, refs, live pages, roots, and whether a work
// spool exists; failed tree writes restore that snapshot and rewind in-place
// work mutations from an extent undo journal (or drop work if the txn created
// it). CommitTo rejects the last committed source before any dest write. It
// writes INVALID_AMAP + sync, body/pages + sync, then VALID_AMAP2 + sync
// (MS-PST 2.6.1.3.7). CommitFile writes a sibling
// temp, renames, and fsyncs the parent directory. A post-rename dirsync or
// reopen failure returns ErrAdopt, keeps the work spool as the usable
// source, and records PendingPath. The payload source is one owned/borrowed
// ioHandle: borrowed readers are never closed, owned files are closed,
// owned temp spools are closed and removed. VALID_AMAP1 is rejected.
// Close/remove failures after a successful dest sync return a cleanup error
// without pointing NDB at a closed old source.
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
