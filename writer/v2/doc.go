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
// it). CommitTo renders a complete image to a fresh owned file-backed
// staging sink (INVALID + sync, body/pages + sync, VALID + sync), installs
// that stage as the authoritative source, then publishes onto dest with the
// same INVALID-first protocol. A dest publish failure keeps the complete
// stage readable and cannot destroy the only valid source through a hidden
// alias. Store.hold retains at most one owned OpenNDBFile source an opaque
// dest may still write through; replaced stages are closed and hold is never
// overwritten. Encode/writeCommit failures join discardStage close/remove.
// sameIO compares only *MemSink/*FileSink/*os.File
// pointers and never uses interface ==. CommitFile writes a sibling
// temp, renames, and fsyncs the parent directory. A post-rename dirsync or
// reopen failure returns ErrAdopt, keeps the work spool as the usable
// source, and records PendingPath. The payload
// source is one owned/borrowed ioHandle: borrowed readers are never closed,
// owned files are closed, owned temp spools are closed and removed. VALID_AMAP1 is rejected.
// Close/remove failures after a successful dest sync return a cleanup error
// without pointing NDB at a closed old source.
// PST-008 is Heap-on-Node: first/normal/fill-level pages over a data tree,
// HNPAGEMAP with cAlloc/cFree/offsets/fill levels, HID numbering (hidIndex
// 1..2047 per page; a new page is started before hidIndex or hidBlockIndex
// overflow), and oversized values as HNID subnodes. Data-tree validation, reconstruction, flattening,
// and lazy readback share one bounded XX->X->data walker.
// PST-009 is BTH-on-HN plus Property Context: unique sorted keys, multi-level
// index nodes, PC scalars/string/binary/MV/object, HID vs HNID-subnode
// placement, unknown properties preserved on Load+Set. Transaction snapshots
// clone storeState/catalogState. Stage/work/hold/adopt share one ioHandle
// ownership boundary; cleanup uses explicit sinkOwn metadata, never Name()
// path shape.
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
