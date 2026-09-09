package writer

// BIDIsInternal reports bidInternal (bit 1). See MS-PST 2.2.2.2.
func BIDIsInternal(bid uint64) bool { return bid&BIDInternal != 0 }

// BIDHasReserved reports the reserved low bit, which MUST be 0.
func BIDHasReserved(bid uint64) bool { return bid&BIDReserved != 0 }

// MakeInternalBID sets bidInternal on a data-block BID (low two bits 0).
func MakeInternalBID(bid uint64) uint64 { return (bid &^ (BIDReserved | BIDInternal)) | BIDInternal }

// takeMonotonic returns *next and adds inc. Page BIDs increment by 1;
// block BIDs increment by 4 so reserved/internal bits stay clear.
func takeMonotonic(next *uint64, inc uint64) (uint64, error) {
	v := *next
	if v == 0 {
		return 0, limitErr("bid", "null next counter (MS-PST %s)", SectionBID)
	}
	if inc > 0 && v > ^uint64(0)-inc {
		return 0, limitErr("bid", "counter overflow: next=0x%x increment=%d (MS-PST %s)", v, inc, SectionBID)
	}
	*next = v + inc
	if *next == 0 {
		return 0, limitErr("bid", "counter wrapped (MS-PST %s)", SectionBID)
	}
	return v, nil
}

// TakePageBID allocates the next page BID. Page BIDs use all bits and
// increment by 1 (MS-PST 2.2.2.2). They are never derived from a file offset.
func (s *SequentialIDs) TakePageBID() (uint64, error) {
	return takeMonotonic(&s.nextPage, PageBIDIncrement)
}

// TakeBlockBID allocates the next external (data) block BID. Increment is 4
// so bid.r and bidInternal stay 0.
func (s *SequentialIDs) TakeBlockBID() (uint64, error) {
	return takeMonotonic(&s.nextBlock, BlockBIDIncrement)
}

// TakeInternalBlockBID allocates a block BID with bidInternal set.
func (s *SequentialIDs) TakeInternalBlockBID() (uint64, error) {
	bid, err := s.TakeBlockBID()
	if err != nil {
		return 0, err
	}
	return MakeInternalBID(bid), nil
}
