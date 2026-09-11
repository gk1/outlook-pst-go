package writer

import (
	"io"
	"os"
)

// sinkOwn is explicit close/remove metadata for a sink. Cleanup never infers
// lifetime from the concrete type or from Name() path shape.
type sinkOwn struct {
	Life   srcLife
	Remove bool
	Path   string
}

// SinkOwner is implemented by sinks that carry ownership metadata.
type SinkOwner interface {
	SinkOwn() sinkOwn
}

func (s *FileSink) SinkOwn() sinkOwn {
	if s == nil {
		return sinkOwn{Life: lifeBorrowed}
	}
	return s.own
}

func (m *MemSink) SinkOwn() sinkOwn {
	return sinkOwn{Life: lifeBorrowed}
}

func ownershipOf(s Sink) sinkOwn {
	if s == nil {
		return sinkOwn{Life: lifeBorrowed}
	}
	if o, ok := s.(SinkOwner); ok {
		return o.SinkOwn()
	}
	// Unknown injected sinks: Close is allowed, removal is not inferred.
	return sinkOwn{Life: lifeClose}
}

func newOwnedFile(f *os.File, own sinkOwn) *FileSink {
	if own.Path == "" && f != nil {
		own.Path = f.Name()
	}
	return &FileSink{f: f, own: own}
}

func tempFileOwn(path string) sinkOwn {
	return sinkOwn{Life: lifeTemp, Remove: true, Path: path}
}

func ownedFileOwn(path string) sinkOwn {
	return sinkOwn{Life: lifeClose, Path: path}
}

func newHandle(s Sink) *ioHandle {
	if s == nil {
		return nil
	}
	own := ownershipOf(s)
	return &ioHandle{r: s, w: s, life: own.Life, remove: own.Remove, path: own.Path}
}

func borrowedHandle(s Sink) *ioHandle {
	if s == nil {
		return nil
	}
	return &ioHandle{r: s, w: s, life: lifeBorrowed}
}

func ownedHandle(s Sink, life srcLife) *ioHandle {
	h := newHandle(s)
	if h == nil {
		return nil
	}
	h.life = life
	if life != lifeTemp {
		h.remove = false
	}
	return h
}

func newHandleReader(r io.ReaderAt, life srcLife) *ioHandle {
	h := &ioHandle{r: r, life: life}
	if sk, ok := r.(Sink); ok {
		h.w = sk
		if life == lifeBorrowed {
			return h
		}
		own := ownershipOf(sk)
		h.life = own.Life
		h.remove = own.Remove
		h.path = own.Path
	}
	return h
}
