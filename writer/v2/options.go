package writer

import (
	"encoding/binary"
	"io"
	"os"
	"time"
)

// Feature is a named capability the v2 writer may reject.
type Feature string

const (
	FeatureANSI            Feature = "ansi-pst"
	FeatureOST             Feature = "ost"
	FeatureSearchFolder    Feature = "search-folder"
	FeatureCalendar        Feature = "calendar"
	FeatureContact         Feature = "contact"
	FeatureTask            Feature = "task"
	FeatureOLE             Feature = "ole-attachment"
	FeatureEmbeddedMessage Feature = "embedded-message"
	FeatureWIPCrypt        Feature = "wip-crypt"
	FeatureInPlaceMutation Feature = "in-place-mutation"
	FeatureCompaction      Feature = "compaction"
	FeatureExistingFile    Feature = "mutate-existing-pst"
)

// Clock supplies timestamps so fixture generation is deterministic.
type Clock interface {
	Now() time.Time
}

// FixedClock always returns T (UTC).
type FixedClock struct{ T time.Time }

func (c FixedClock) Now() time.Time {
	if c.T.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return c.T.UTC()
}

// IDSource supplies monotonic identifiers. All v2 codecs must use this instead
// of wall-clock, maps, or file offsets as IDs.
type IDSource interface {
	NextNID(nidType byte) uint32
	NextBlockBID() uint64
	NextPageBID() uint64
	NextUnique() uint32
	NextSearchKey() [16]byte
	NextRecordKey() [16]byte
}

// SequentialIDs is a deterministic IDSource.
// NID indexes start at StartIndex (default 0x400) so special NIDs stay free.
type SequentialIDs struct {
	StartIndex uint32
	nid        [32]uint32
	nextBlock  uint64
	nextPage   uint64
	unique     uint32
	keys       uint64
}

// NewSequentialIDs returns counters matching a blank Unicode PST.
func NewSequentialIDs() *SequentialIDs {
	s := &SequentialIDs{
		StartIndex: 0x400,
		nextBlock:  4, // BIDs 0-3 are reserved
		nextPage:   4,
		unique:     1,
	}
	for i := range s.nid {
		s.nid[i] = s.StartIndex
	}
	return s
}

func (s *SequentialIDs) NextNID(nidType byte) uint32 {
	t := nidType & 0x1F
	idx := s.nid[t]
	s.nid[t] = idx + 1
	return MakeNID(t, idx)
}

func (s *SequentialIDs) NextBlockBID() uint64 {
	id, err := s.TakeBlockBID()
	if err != nil {
		return 0
	}
	return id
}

func (s *SequentialIDs) NextPageBID() uint64 {
	id, err := s.TakePageBID()
	if err != nil {
		return 0
	}
	return id
}

func (s *SequentialIDs) NextUnique() uint32 {
	v := s.unique
	s.unique++
	return v
}

func (s *SequentialIDs) nextKey() [16]byte {
	s.keys++
	var k [16]byte
	binary.BigEndian.PutUint64(k[8:], s.keys)
	return k
}

func (s *SequentialIDs) NextSearchKey() [16]byte { return s.nextKey() }
func (s *SequentialIDs) NextRecordKey() [16]byte { return s.nextKey() }

// Limits bound export size. Zero means DefaultLimits.
type Limits struct {
	MaxFolders               int
	MaxFolderDepth           int
	MaxMessages              int
	MaxRecipientsPerMessage  int
	MaxAttachmentsPerMessage int
	MaxMessageBytes          int64
	MaxAttachmentBytes       int64
	MaxFileBytes             int64
}

// DefaultLimits are conservative mailbox-export caps, not MS-PST maxima.
func DefaultLimits() Limits {
	return Limits{
		MaxFolders:               100_000,
		MaxFolderDepth:           64,
		MaxMessages:              1_000_000,
		MaxRecipientsPerMessage:  4096,
		MaxAttachmentsPerMessage: 1024,
		MaxMessageBytes:          256 << 20,
		MaxAttachmentBytes:       128 << 20,
		MaxFileBytes:             50 << 30,
	}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxFolders == 0 {
		l.MaxFolders = d.MaxFolders
	}
	if l.MaxFolderDepth == 0 {
		l.MaxFolderDepth = d.MaxFolderDepth
	}
	if l.MaxMessages == 0 {
		l.MaxMessages = d.MaxMessages
	}
	if l.MaxRecipientsPerMessage == 0 {
		l.MaxRecipientsPerMessage = d.MaxRecipientsPerMessage
	}
	if l.MaxAttachmentsPerMessage == 0 {
		l.MaxAttachmentsPerMessage = d.MaxAttachmentsPerMessage
	}
	if l.MaxMessageBytes == 0 {
		l.MaxMessageBytes = d.MaxMessageBytes
	}
	if l.MaxAttachmentBytes == 0 {
		l.MaxAttachmentBytes = d.MaxAttachmentBytes
	}
	if l.MaxFileBytes == 0 {
		l.MaxFileBytes = d.MaxFileBytes
	}
	return l
}

// Sink is the output for a later codec. Seek and WriteAt are required so
// HEADER/AMap pages can be rewritten at commit. Ordinary unit tests use MemSink.
type Sink interface {
	io.Writer
	io.WriterAt
	io.ReaderAt
	io.Seeker
	Sync() error
	Close() error
	Name() string
}

// MemSink is an in-memory sink.
type MemSink struct {
	name string
	buf  []byte
	off  int64
}

func NewMemSink(name string) *MemSink {
	if name == "" {
		name = "memory"
	}
	return &MemSink{name: name}
}

func (m *MemSink) Name() string { return m.name }

func (m *MemSink) Bytes() []byte {
	out := make([]byte, len(m.buf))
	copy(out, m.buf)
	return out
}

func (m *MemSink) grow(n int64) {
	if n > int64(len(m.buf)) {
		nb := make([]byte, n)
		copy(nb, m.buf)
		m.buf = nb
	}
}

func (m *MemSink) Write(p []byte) (int, error) {
	end := m.off + int64(len(p))
	m.grow(end)
	copy(m.buf[m.off:], p)
	m.off = end
	return len(p), nil
}

func (m *MemSink) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, invalidArg("offset", "negative WriteAt offset %d", off)
	}
	m.grow(off + int64(len(p)))
	copy(m.buf[off:], p)
	return len(p), nil
}

func (m *MemSink) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, invalidArg("offset", "negative ReadAt offset %d", off)
	}
	if off >= int64(len(m.buf)) {
		return 0, io.EOF
	}
	n := copy(p, m.buf[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (m *MemSink) Truncate(n int64) error {
	if n < 0 {
		return invalidArg("size", "negative Truncate %d", n)
	}
	if n > int64(len(m.buf)) {
		m.grow(n)
		return nil
	}
	m.buf = m.buf[:n]
	if m.off > n {
		m.off = n
	}
	return nil
}

func (m *MemSink) Seek(offset int64, whence int) (int64, error) {
	var n int64
	switch whence {
	case io.SeekStart:
		n = offset
	case io.SeekCurrent:
		n = m.off + offset
	case io.SeekEnd:
		n = int64(len(m.buf)) + offset
	default:
		return 0, invalidArg("whence", "invalid seek whence %d", whence)
	}
	if n < 0 {
		return 0, invalidArg("offset", "negative seek")
	}
	m.off = n
	return n, nil
}

func (m *MemSink) Sync() error  { return nil }
func (m *MemSink) Close() error { return nil }

// FileSink wraps an os.File.
type FileSink struct{ f *os.File }

func CreateFileSink(path string) (*FileSink, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, &Error{Code: CodeIO, Detail: err.Error(), Err: err}
	}
	return &FileSink{f: f}, nil
}

func (s *FileSink) Write(p []byte) (int, error)                  { return s.f.Write(p) }
func (s *FileSink) WriteAt(p []byte, off int64) (int, error)     { return s.f.WriteAt(p, off) }
func (s *FileSink) ReadAt(p []byte, off int64) (int, error)      { return s.f.ReadAt(p, off) }
func (s *FileSink) Seek(offset int64, whence int) (int64, error) { return s.f.Seek(offset, whence) }
func (s *FileSink) Sync() error                                  { return s.f.Sync() }
func (s *FileSink) Close() error                                 { return s.f.Close() }
func (s *FileSink) Name() string                                 { return s.f.Name() }
func (s *FileSink) Truncate(n int64) error                       { return s.f.Truncate(n) }

// Options configure a v2 exporter.
type Options struct {
	Clock       Clock
	IDs         IDSource
	Sink        Sink
	Limits      Limits
	Crypt       byte
	DisplayName string
}

func (o Options) withDefaults() Options {
	if o.Clock == nil {
		o.Clock = FixedClock{T: time.Unix(0, 0).UTC()}
	}
	if o.IDs == nil {
		o.IDs = NewSequentialIDs()
	}
	if o.Sink == nil {
		o.Sink = NewMemSink("plan")
	}
	o.Limits = o.Limits.withDefaults()
	if o.DisplayName == "" {
		o.DisplayName = "openBackup Export"
	}
	return o
}
